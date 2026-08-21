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
	"github.com/operatinggraph/lattice/internal/descriptorform"
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

	// The read boundary (D1.5). pgPool is the protected clinicAppointmentsRead
	// read-model pool; nil when CLINIC_APP_PG_DSN is unset → protected reads
	// return a clean 502 rather than panicking. authn is the session cookie's
	// verifier, held here so the health probe can report whether an auth
	// posture is configured at all; nil ⇒ every session-gated request 401s
	// (fail closed).
	pgPool *pgxpool.Pool
	authn  *auth.Authenticator

	// session serves the login/logout/whoami/refresh surface and resolves every
	// request to a signed-in identity (internal/appsession).
	session *appsession.Manager

	// gatewayURL is the Gateway's externally-reachable base URL (e.g.
	// http://localhost:8080), served to the FE via GET /api/config so it can
	// submit writes browser-direct (real-actor-write-auth-e2e-design.md §3.1)
	// instead of proxying through /api/op.
	gatewayURL string
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
		panic("clinic-app: embed web sub-fs: " + err.Error())
	}
	inner := http.NewServeMux()
	inner.Handle("/", http.FileServer(http.FS(sub)))
	inner.Handle("/shared/", http.StripPrefix("/shared/", http.FileServer(descriptorform.FS())))

	inner.HandleFunc("/api/op-catalog", s.handleOpCatalog)
	inner.HandleFunc("/api/providers", s.handleProviders)
	inner.HandleFunc("/api/sites", s.handleSites)
	inner.HandleFunc("/api/provider-sites", s.handleProviderSites)
	inner.HandleFunc("/api/residents", s.handleResidents)
	inner.HandleFunc("/api/staff/patients", s.handleStaffPatients)
	inner.HandleFunc("/api/appointments", s.handleAppointments)
	inner.HandleFunc("/api/my-appointments", s.handleMyAppointments)
	inner.HandleFunc("/api/my-schedule", s.handleMyProviderSchedule)
	inner.HandleFunc("/api/my-encounters", s.handleMyEncounters)
	inner.HandleFunc("/api/staff/appointments", s.handleStaffAppointments)
	inner.HandleFunc("/api/my-visit-series", s.handleMyVisitSeries)
	inner.HandleFunc("/api/staff/visit-series", s.handleStaffVisitSeries)
	inner.HandleFunc("/api/ledger", s.handleLedger)
	inner.HandleFunc("/api/wellness/sessions", s.handleWellnessSessions)
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
