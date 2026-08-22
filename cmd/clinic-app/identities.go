package main

import (
	"context"
	"net/http"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// The identity-name read boundary: handleIdentities serves clinicIdentitiesRead
// as the SIGNED-IN actor, mirroring cmd/wellness-app's and cmd/cafe-app's
// handleIdentities. The lens is a Secure Lens (Contract #3 §3.10): the identity
// `name` is a sensitive aspect the Refractor decrypts into this RLS-protected
// table alone, so an unauthenticated full-name roster cannot exist.
//
// Each row is SELF-anchored on the identity's own bare NanoID
// (packages/clinic-domain/lenses.go), so a signed-in actor reads their OWN row
// via the platform's base cap-read self-grant — no clinic-side grant producer
// is involved, unlike the patient- and provider-anchored reads. The FE reads
// this to resolve "Signed in as <name>" for the front-desk hat, whose identity
// is neither a patient nor a bound provider and therefore appears in neither
// roster.

// protectedIdentityRow is one row of the clinicIdentitiesRead protected
// Postgres read model, as scanned from the RLS-scoped read. NAME only — no
// additional PII.
type protectedIdentityRow struct {
	IdentityKey string `json:"identityKey"`
	Name        string `json:"name"`
}

// selectIdentitiesSQL reads the protected model. It carries NO auth WHERE —
// the RLS policy (FORCE ROW LEVEL SECURITY + the §6.14 set-membership policy)
// injects the actor scope from the txn-local lattice.actor_id session
// variable. `name IS NOT NULL` is a display filter, not authorization: the
// Secure Lens projects NULL for a crypto-shredded identity's name.
const selectIdentitiesSQL = `
SELECT identity_key, name
FROM read_clinic_identities
WHERE name IS NOT NULL
ORDER BY name, identity_key`

// queryIdentities runs the protected read inside a per-request transaction
// with a txn-local actor session variable — the same pooling-safety discipline
// as queryPatients / queryMyAppointments (SET LOCAL is discarded at
// COMMIT/ROLLBACK, so the pooled connection inherits no actor across
// requests). The query itself carries no auth filter; RLS is the scope.
func queryIdentities(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedIdentityRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectIdentitiesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedIdentityRow, 0)
	for rows.Next() {
		var row protectedIdentityRow
		if err := rows.Scan(&row.IdentityKey, &row.Name); err != nil {
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

// handleIdentities implements GET /api/identities: the signed-in actor's own
// name (the self-anchor), plus the whole roster for a WildcardAnchor holder.
func (s *server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-domain protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryIdentities(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected clinic identities", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected identities model")
		return
	}
	resp := s.withProjectionHealth(ctx, pkgmgr.LensID("clinic-domain", "clinicIdentitiesRead"),
		map[string]any{"identities": rows, "count": len(rows), "scope": "rls"})
	s.writeJSON(w, http.StatusOK, resp)
}
