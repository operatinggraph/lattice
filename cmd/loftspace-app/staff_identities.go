package main

import (
	"context"
	"net/http"
)

// The applicant-roster read boundary (D1.5): handleStaffIdentities serves the
// roster from the PROTECTED applicantRosterRead Postgres model as the SIGNED-IN
// actor — mirroring cmd/clinic-app's handleStaffPatients / clinicPatientsRead.
// That model is the ONLY roster surface: the identity name is a sensitive aspect
// the Refractor Secure Lens decrypts into this RLS-protected table alone
// (Contract #3 §3.10), so an unauthenticated full-name roster (a system-wide
// membership disclosure) cannot exist.
//
// Like the clinic patient roster there is no per-identity self-anchor to carve
// out — "the whole roster" has no single-row owner — so every row projects an
// EMPTY authz_anchors set: only an actor holding the reserved WildcardAnchor
// grant ever matches. A session without it therefore reads an empty roster, and
// the surfaces that decorate keys with names fall back to the keys.

// protectedIdentityRow is one row of the applicantRosterRead protected
// Postgres read model, as scanned from the RLS-scoped read. NAME + STATE
// only — no additional PII.
type protectedIdentityRow struct {
	IdentityKey string `json:"identityKey"`
	Name        string `json:"name"`
	State       string `json:"state"`
}

// selectIdentitiesSQL reads the protected model. It carries NO auth WHERE —
// the RLS policy (FORCE ROW LEVEL SECURITY + the §6.14 set-membership policy)
// injects the actor scope from the txn-local lattice.actor_id session
// variable. `name IS NOT NULL` is a display filter, not authorization: the
// Secure Lens projects NULL for a crypto-shredded identity's name, which has
// no place in a picker (and would fail the plain-string Scan below). `state`
// is COALESCEd because the lens projects it unconditionally — an identity
// carrying a `.name` but no `.state` aspect projects NULL there, which must
// degrade to an empty string, not fail the whole roster Scan. Sorted by name
// for a stable picker.
const selectIdentitiesSQL = `
SELECT identity_key, name, COALESCE(state, '') AS state
FROM read_loftspace_identities
WHERE name IS NOT NULL
ORDER BY name, identity_key`

// queryIdentities runs the protected read inside a per-request transaction
// with a txn-local actor session variable — the same pooling-safety
// discipline as queryApplications / queryLandlordApplications (SET LOCAL is
// discarded at COMMIT, so the pooled connection returns clean for the next
// request). The query itself carries no auth filter; RLS is the scope.
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
		if err := rows.Scan(&row.IdentityKey, &row.Name, &row.State); err != nil {
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

// handleStaffIdentities implements GET /api/staff/identities — the applicant
// picker, PROTECTED and RLS-scoped (D1.5).
func (s *server) handleStaffIdentities(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("identities protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the loftspace-domain protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryIdentities(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected loftspace identities", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected identities model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"identities": rows, "count": len(rows), "scope": "rls"})
}
