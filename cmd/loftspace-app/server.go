package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

//go:embed web
var webFS embed.FS

// server holds the dependencies the HTTP handlers share. conn may be nil when
// NATS was unreachable at startup; every handler checks requireConn first and
// returns a JSON error rather than dereferencing a nil connection.
type server struct {
	conn            *substrate.Conn
	bootstrapLoaded bool
	logger          *slog.Logger
	natsTimeout     time.Duration
	// uploadCap bounds a single document upload (OBJECTS_MAX_UPLOAD_BYTES); the
	// substrate ObjectPut enforces it as the authoritative per-blob limit.
	uploadCap int64
	// gatewayURL is the Gateway's externally-reachable base URL (e.g.
	// http://localhost:8080), served to the FE via GET /api/config so it can
	// submit writes browser-direct (real-actor-write-auth-e2e-design.md §3.1)
	// instead of proxying through /api/op.
	gatewayURL string

	// The read boundary (D1.3 Fire 3). pgPool is the protected lease-applications
	// read-model pool; nil when LOFTSPACE_APP_PG_DSN is unset → protected reads
	// return a clean 502 rather than panicking. authn is the session cookie's
	// verifier, held here so the health probe can report whether an auth
	// posture is configured at all; nil ⇒ every session-gated request 401s
	// (fail closed).
	pgPool *pgxpool.Pool
	authn  *auth.Authenticator

	// session serves the login/logout/whoami/refresh surface and resolves every
	// request to a signed-in identity (internal/appsession). signer is that
	// surface's demo-posture minter, held here for the one ceremony that needs a
	// credential OTHER than the signed-in one: completing a credential link
	// (credentials_link.go). Nil in the production verify-only posture, where
	// that ceremony reports 404.
	session *appsession.Manager
	signer  *appsession.Signer
}

// pgxBeginner is the subset of *pgxpool.Pool the protected read uses — a single
// Begin so the query path can be unit-tested with a fake transaction.
type pgxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embed guarantees web/ exists at build time; a failure here is a
		// programmer error, not a runtime condition.
		panic("loftspace-app: embed web sub-fs: " + err.Error())
	}
	inner := http.NewServeMux()
	inner.Handle("/", http.FileServer(http.FS(sub)))

	inner.HandleFunc("/api/listings", s.handleListings)
	inner.HandleFunc("/api/staff/identities", s.handleStaffIdentities)
	inner.HandleFunc("/api/applications", s.handleApplications)
	inner.HandleFunc("/api/credentials", s.handleCredentials)
	inner.HandleFunc("/api/credentials/link/complete", s.handleCompleteCredentialLink)
	inner.HandleFunc("/api/unit-applications", s.handleUnitApplications)
	inner.HandleFunc("/api/landlord/applications", s.handleLandlordApplications)
	inner.HandleFunc("/api/portfolio-pulse", s.handlePortfolioPulse)
	inner.HandleFunc("/api/renewals", s.handleRenewals)
	inner.HandleFunc("/api/search", s.handleSearch)
	inner.HandleFunc("/api/tasks", s.handleTasks)
	inner.HandleFunc("/api/objects", s.handleObjects)
	inner.HandleFunc("/api/objects/", s.handleObjects)
	inner.HandleFunc("/api/lease-document", s.handleLeaseDocument)
	inner.HandleFunc("/api/ledger", s.handleLedger)
	inner.HandleFunc("/api/one-bill", s.handleOneBillStatement)
	inner.HandleFunc("/api/config", s.handleConfig)

	s.session.RegisterRoutes(inner)
	mux.Handle("/", s.session.RequireSession(inner))
}

// handleConfig implements GET /api/config: the FE's one bit of runtime
// configuration, the Gateway base URL it submits writes to browser-direct.
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"gatewayUrl": s.gatewayURL})
}

// writeJSON encodes v as JSON with the given status code.
func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("encode response", "error", err)
	}
}

// writeError sends {"error": msg} with the given status code. status is 502 for
// an upstream/NATS failure and 400 for a bad request.
func (s *server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

// requireConn returns the live connection, or writes a JSON 502 and returns
// false when NATS was never connected — the single guard that keeps a NATS-down
// deployment from panicking on a nil *substrate.Conn.
func (s *server) requireConn(w http.ResponseWriter) (*substrate.Conn, bool) {
	if s.conn == nil {
		s.writeError(w, http.StatusBadGateway, "NATS is not connected; check NATS_URL and that the deployment is up")
		return nil, false
	}
	return s.conn, true
}

// reqContext bounds a handler's NATS work by the server's per-request timeout,
// derived from the incoming request's context so a client disconnect cancels.
func (s *server) reqContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.natsTimeout)
}
