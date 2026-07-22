package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

// PgPool is the subset of *pgxpool.Pool a read-model query needs — a single
// Begin, so the RLS path is exercisable against a real Postgres fixture or a
// fake in unit tests.
type PgPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ReadModel is one registered GET /v1/<name> surface (design §3.2/§8 Fire
// 3): a fixed, operator-authored SELECT with NO caller-supplied predicate —
// Postgres RLS (Contract #6 §6.14) does the row filtering via the txn-local
// lattice.actor_id session variable queryReadModel sets. The Gateway carries
// no compiled knowledge of any read-model's row shape (mirrors the bridge's
// type-agnostic adapter seam): rows are scanned by column name and served as
// JSON verbatim.
type ReadModel struct {
	Query string
}

// ConfigureReadModels attaches the Postgres pool + the read-model registry
// to an already-built Server. Call before RegisterRoutes — routes are
// mounted once, at registration time, from whatever is configured here.
// pool may be nil (every read then 502s "read model unavailable" rather than
// panicking, mirroring the write path's requireConn discipline); models may
// be empty or nil (no /v1/<name> routes are mounted).
func (s *Server) ConfigureReadModels(pool PgPool, models map[string]ReadModel) {
	s.pgPool = pool
	s.readModels = models
}

// ValidReadModelName reports whether name is safe to mount verbatim as an
// HTTP path segment and does not collide with the write-path keystone.
// Exported so a read-model loader (e.g. cmd/gateway's directory loader) can
// validate a name before ever handing it to ConfigureReadModels.
func ValidReadModelName(name string) bool {
	if name == "" || name == "operations" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// handleReadModel builds the GET /v1/<name> handler for model.
func (s *Server) handleReadModel(name string, model ReadModel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		s.metrics.readsTotal.Add(1)

		token, ok := bearerToken(r)
		if !ok {
			s.metrics.authFailuresTotal.Add(1)
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization: Bearer header")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.reqTimeout)
		defer cancel()

		actor, err := s.authn.Authenticate(ctx, token)
		if err != nil {
			s.metrics.authFailuresTotal.Add(1)
			status, msg := mapAuthError(err)
			writeError(w, status, msg)
			return
		}
		// Defense in depth: RLS keys off actor.Subject (set_config below). A
		// verifier that admits a subjectless token must never reach the read
		// path — mirrors loftspace-app's authenticateRead guard.
		if strings.TrimSpace(actor.Subject) == "" {
			s.metrics.authFailuresTotal.Add(1)
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}

		if s.pgPool == nil {
			s.metrics.readFailuresTotal.Add(1)
			writeError(w, http.StatusBadGateway, "read model unavailable")
			return
		}

		// A claimed credential (A) reads as its business identity (U) — the
		// same shared-seam resolution the write path applies
		// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5
		// R1), so RLS scopes rows to U's links, not A's.
		resolvedSubject := strings.TrimPrefix(s.resolveActor(ctx, actor.ActorID), auth.IdentityKeyPrefix)
		rows, err := queryReadModel(ctx, s.pgPool, model.Query, resolvedSubject)
		if err != nil {
			s.metrics.readFailuresTotal.Add(1)
			s.logger.Error("gateway: read model query failed", "readModel", name, "error", err)
			writeError(w, http.StatusBadGateway, "read model query failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "count": len(rows)})
	}
}

// queryReadModel runs query inside a per-request transaction with a
// txn-local actor session variable — set_config(..., is_local=true) is
// discarded at COMMIT, so a pooled connection returns clean (no actor => RLS
// deny-all per Contract #6 §6.14 FORCE ROW LEVEL SECURITY) for whichever
// request the connection serves next. Rows are scanned generically by
// column name so this function carries no knowledge of any read-model's row
// shape — the caller-facing JSON keys are exactly the query's column names.
func queryReadModel(ctx context.Context, pool PgPool, query, actorSubject string) ([]map[string]any, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorSubject); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(fields))
		for i, f := range fields {
			row[f.Name] = vals[i]
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
