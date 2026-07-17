package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

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

	// devSigner mints Bearer tokens the FE presents to the Gateway; nil
	// unless WELLNESS_APP_DEV_AUTH is enabled. It mints the fixed staff token
	// (this app's own admin actor) for operator-scoped writes, and — via
	// handleDevToken — a per-resident token for the consumer scope=self
	// CreateBooking/CancelBooking self-service path.
	devSigner *devSigner

	// gatewayURL is the Gateway's externally-reachable base URL, served to
	// the FE via GET /api/config so it can submit writes browser-direct.
	gatewayURL string
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embed guarantees web/ exists at build time; a failure here is a
		// programmer error, not a runtime condition.
		panic("wellness-app: embed web sub-fs: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/studios", s.handleStudios)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/bookings", s.handleBookings)
	mux.HandleFunc("/api/residents", s.handleResidents)
	mux.HandleFunc("/api/staff/dev-token", s.handleStaffDevToken)
	mux.HandleFunc("/api/dev-token", s.handleDevToken)
	mux.HandleFunc("/api/config", s.handleConfig)
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

// writeError sends {"error": msg} with the given status code.
func (s *server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

// requireConn returns the live connection, or writes a JSON 502 and returns
// false when NATS was never connected.
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

// kvGetter reads one key from a lens bucket, returning (value, found).
type kvGetter func(key string) ([]byte, bool)

func (s *server) kvGetter(ctx context.Context, bucket string) kvGetter {
	return func(key string) ([]byte, bool) {
		entry, err := s.conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
}
