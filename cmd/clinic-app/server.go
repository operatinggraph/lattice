package main

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/substrate"
)

//go:embed web
var webFS embed.FS

// server holds the dependencies the HTTP handlers share. conn may be nil when
// NATS was unreachable at startup; every handler checks requireConn first and
// returns a JSON error rather than dereferencing a nil connection.
type server struct {
	conn        *substrate.Conn
	adminActor  string
	logger      *slog.Logger
	natsTimeout time.Duration

	// The read boundary (D1.5). pgPool is the protected clinicAppointmentsRead
	// read-model pool; nil when CLINIC_APP_PG_DSN is unset → protected reads
	// return a clean 502 rather than panicking. authn verifies the read actor's
	// JWT; nil when no auth posture is configured → protected reads 401 (fail
	// closed). devSigner mints demo tokens; nil unless CLINIC_APP_DEV_AUTH.
	pgPool    *pgxpool.Pool
	authn     *auth.Authenticator
	devSigner *devSigner
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
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/providers", s.handleProviders)
	mux.HandleFunc("/api/patients", s.handlePatients)
	mux.HandleFunc("/api/appointments", s.handleAppointments)
	mux.HandleFunc("/api/my-appointments", s.handleMyAppointments)
	mux.HandleFunc("/api/my-schedule", s.handleMyProviderSchedule)
	mux.HandleFunc("/api/staff/appointments", s.handleStaffAppointments)
	mux.HandleFunc("/api/staff/dev-token", s.handleStaffDevToken)
	mux.HandleFunc("/api/my-visit-series", s.handleMyVisitSeries)
	mux.HandleFunc("/api/staff/visit-series", s.handleStaffVisitSeries)
	mux.HandleFunc("/api/op", s.handleOp)
	mux.HandleFunc("/api/dev-token", s.handleDevToken)
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

// requireBody reads up to 1 MiB of the request body, the cap for the small JSON
// op payloads this app submits.
func requireBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}
