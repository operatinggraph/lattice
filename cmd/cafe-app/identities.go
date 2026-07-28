package main

import (
	"context"
	"net/http"
)

// The identity-name read boundary: handleIdentities serves cafeIdentitiesRead
// as the SIGNED-IN actor — mirroring cmd/loftspace-app's
// handleStaffIdentities / applicantRosterRead. The lens is a Secure Lens
// (Contract #3 §3.10): the identity `name` is a sensitive aspect the
// Refractor decrypts into this RLS-protected table alone, so an
// unauthenticated full-name roster cannot exist.
//
// Unlike loftspace's applicantRosterRead, each row is SELF-anchored on the
// identity's own bare NanoID (packages/cafe-domain/lenses.go), so an ordinary
// signed-in resident reads their OWN row via the platform's base cap-read
// self-grant — not only an actor holding the reserved WildcardAnchor grant.
// The FE reads this to resolve "Signed in as <name>" for every hat, not just
// staff.

// protectedIdentityRow is one row of the cafeIdentitiesRead protected
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
FROM read_cafe_identities
WHERE name IS NOT NULL
ORDER BY name, identity_key`

// queryIdentities runs the protected read inside a per-request transaction
// with a txn-local actor session variable — the same pooling-safety
// discipline as cmd/loftspace-app's queryIdentities (SET LOCAL is discarded
// at COMMIT, so the pooled connection returns clean for the next request).
// The query itself carries no auth filter; RLS is the scope.
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
// name (self-anchor), plus the whole roster for a WildcardAnchor holder.
func (s *server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	actorID, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("identities protected read requested but pgPool is nil (set CAFE_APP_PG_DSN + ensure Postgres and the cafe-domain protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryIdentities(ctx, s.pgPool, actorID)
	if err != nil {
		s.logger.Error("read protected cafe identities", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected identities model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"identities": rows, "count": len(rows), "scope": "rls"})
}
