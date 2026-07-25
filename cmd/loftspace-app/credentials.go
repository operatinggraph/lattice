package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// credentialEntry is one bound credential the account-settings page can
// unlink — {actorKey, boundAt}, the shape identity-domain's credentialBinding
// aspect stores per entry.
type credentialEntry struct {
	ActorKey string `json:"actorKey"`
	BoundAt  string `json:"boundAt"`
}

// credentialBindingData is the decrypted shape of the identityCredentialsRead
// Secure Lens's `binding` column — the whole credentialBinding aspect, secure
// columns project the entire object (no single scalar field to select). A
// pre-Fire-2 record with no `credentials` array falls back to the singular
// actorKey/boundAt fields (packages/identity-domain/ddls.go's own fallback
// note), mirroring the Starlark script's own read-side fallback.
type credentialBindingData struct {
	ActorKey    string            `json:"actorKey"`
	BoundAt     string            `json:"boundAt"`
	Credentials []credentialEntry `json:"credentials"`
}

func (d credentialBindingData) entries() []credentialEntry {
	if len(d.Credentials) > 0 {
		return d.Credentials
	}
	if d.ActorKey == "" {
		return nil
	}
	return []credentialEntry{{ActorKey: d.ActorKey, BoundAt: d.BoundAt}}
}

// selectIdentityCredentialsSQL reads the protected identityCredentialsRead
// model. No auth WHERE — RLS (the identity's own NanoID as authz_anchor)
// scopes it to the caller's txn-local lattice.actor_id, mirroring
// selectApplicationsSQL.
const selectIdentityCredentialsSQL = `
SELECT entity_key, binding
FROM read_identity_credentials
WHERE entity_key = $1`

// queryIdentityCredentials runs the protected read for one identity inside a
// per-request transaction with a txn-local actor session variable (the same
// pooling-safety discipline queryApplications uses). actorID must be the
// caller's own bare identity NanoID — the only identity_id RLS ever lets it
// see, so the entity_key WHERE is redundant with RLS but keeps the query
// shape explicit and self-documenting.
func queryIdentityCredentials(ctx context.Context, pool pgxBeginner, actorID string) ([]credentialEntry, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, false, err
	}

	var entityKey string
	var bindingJSON []byte
	err = tx.QueryRow(ctx, selectIdentityCredentialsSQL, "vtx.identity."+actorID).Scan(&entityKey, &bindingJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, tx.Commit(ctx)
		}
		return nil, false, err
	}

	var data credentialBindingData
	if err := json.Unmarshal(bindingJSON, &data); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return data.entries(), true, nil
}

// handleCredentials implements GET /api/credentials — the account-settings
// page's "which sign-in methods are linked to me" list, served from the
// PROTECTED identityCredentialsRead Secure Lens as the AUTHENTICATED caller.
// Only ever the caller's own row (RLS); no client-supplied identity param.
func (s *server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	// credentialBinding is a SENSITIVE aspect (Contract #3 §3.10), so this
	// surface serves only a caller who PROVED which identity they are. A
	// boot-env fallback identity proves nothing — it hands the process's
	// identity to anyone who connects — and RLS cannot tell that the caller is
	// not that identity, only that the row belongs to it.
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to manage sign-in methods")
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set LOFTSPACE_APP_PG_DSN and ensure Postgres + the identity-domain protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	entries, found, err := queryIdentityCredentials(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected identity credentials", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected identity-credentials model")
		return
	}
	if !found {
		entries = []credentialEntry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"credentials": entries, "count": len(entries)})
}
