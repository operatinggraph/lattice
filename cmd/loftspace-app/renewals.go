package main

import (
	"context"
	"net/http"
)

// The renewals read boundary (R3, the lease-renewal goal-authored-target
// design's FE increment). Sibling of applications.go/landlord_applications.go:
// identical verified-JWT → per-request txn → SET LOCAL lattice.actor_id → RLS
// path, but read_renewals is DUAL-anchored (design §4.5, packages/lease-signing
// renewalsReadSpec) — a row's authz_anchors carries BOTH the tenant's and the
// managing landlord's NanoID, so the SAME query serves both audiences: a
// signed-in tenant sees their own renewal cycles, a signed-in landlord sees the
// cycles for units they manage, and RLS (not a client-supplied role param)
// decides which rows come back for whom.
//
// There is no separate tenant/landlord endpoint and no cap-read grant lens: the
// primordial cap-read self-grant already grants every identity its own NanoID,
// and the §6.14 set-membership policy returns a row whenever the reading
// actor's NanoID is IN that row's authz_anchors set.

// renewalRow is one row of read_renewals, as scanned from the RLS-scoped read.
// Nullable columns mirror the protected-lens convention (protectedLandlordRow):
// a pointer stays nil rather than projecting a zero value for "not set yet".
type renewalRow struct {
	EntityKey           string   `json:"entityKey"`
	LeaseApp            string   `json:"leaseApp"`
	Tenant              string   `json:"tenant"`
	Landlord            string   `json:"landlord"`
	Status              string   `json:"status"`
	CycleEnd            *string  `json:"cycleEnd"`
	UnitAddress         *string  `json:"unitAddress"`
	RentAmount          *float64 `json:"rentAmount"`
	TermMonths          *float64 `json:"termMonths"`
	TermsSetAt          *string  `json:"termsSetAt"`
	HasGuarantor        *bool    `json:"hasGuarantor"`
	GuarantorVerifiedAt *string  `json:"guarantorVerifiedAt"`
	GuarantorMethod     *string  `json:"guarantorMethod"`
	SignedAt            *string  `json:"signedAt"`
	CancelReason        *string  `json:"cancelReason"`
}

// selectRenewalsSQL reads the protected renewals model. It carries NO auth
// WHERE — the RLS policy (FORCE ROW LEVEL SECURITY + the set-membership
// policy) injects the tenant-or-landlord scope from the txn-local
// lattice.actor_id session variable. Rows sort by (status, entity_key) so an
// open cycle needing action surfaces before a completed/cancelled one.
const selectRenewalsSQL = `
SELECT entity_key, lease_app, tenant, landlord, status, cycle_end,
       unit_address, rent_amount, term_months, terms_set_at,
       has_guarantor, guarantor_verified_at, guarantor_method,
       signed_at, cancel_reason
FROM read_renewals
ORDER BY (status <> 'open'), entity_key`

// queryRenewals runs the protected renewals read inside a per-request
// transaction with a txn-local actor session variable — the same
// pooling-safety pattern queryApplications/queryLandlordApplications use:
// set_config(..., is_local=true) is discarded at COMMIT so the pooled
// connection returns clean for the next request. actorID must be the bare
// NanoID (VerifiedActor.Subject) — it may match a row's tenant OR landlord
// anchor; the caller does not know or declare which in advance.
func queryRenewals(ctx context.Context, pool pgxBeginner, actorID string) ([]renewalRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectRenewalsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]renewalRow, 0)
	for rows.Next() {
		var row renewalRow
		if err := rows.Scan(
			&row.EntityKey, &row.LeaseApp, &row.Tenant, &row.Landlord, &row.Status, &row.CycleEnd,
			&row.UnitAddress, &row.RentAmount, &row.TermMonths, &row.TermsSetAt,
			&row.HasGuarantor, &row.GuarantorVerifiedAt, &row.GuarantorMethod,
			&row.SignedAt, &row.CancelReason,
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

// handleRenewals implements GET /api/renewals — the AUTHENTICATED caller's own
// renewal cycles (as tenant OR managing landlord), read from the PROTECTED
// read_renewals model. The FE tells the two audiences apart client-side by
// comparing each row's tenant/landlord key to the caller's own identity (the
// session subject) — RLS has already ensured every row returned belongs to
// one of those two roles for this actor; there is no third case.
func (s *server) handleRenewals(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("renewals protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the lease-signing protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryRenewals(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected renewals model", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected renewals model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"renewals": rows,
		"count":    len(rows),
		"self":     actor.Subject,
		"scope":    "rls",
	})
}
