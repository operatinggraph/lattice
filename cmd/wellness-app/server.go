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

// publicReadPaths are the reads that answer with no session at all: the class
// schedule (persona-worlds-design.md §7.3). Both project class name, time,
// capacity, studio and instructor display name — no person-identifying column
// about the people their rows are ABOUT — which is the tier that stays open
// on plain NATS-KV. Every other read is gated and scoped to the session's
// subject. Passed to the kit as ExtraExemptPaths.
var publicReadPaths = []string{"/api/studios", "/api/sessions"}

// server holds the dependencies the HTTP handlers share. conn may be nil when
// NATS was unreachable at startup; every handler checks requireConn first and
// returns a JSON error rather than dereferencing a nil connection.
type server struct {
	conn            *substrate.Conn
	bootstrapLoaded bool
	logger          *slog.Logger
	natsTimeout     time.Duration

	// authn is the session cookie's verifier, held here so the health probe
	// can report whether an auth posture is configured at all; nil ⇒ every
	// session-gated request 401s (fail closed).
	authn *auth.Authenticator

	// session serves the login/logout/whoami/refresh surface and resolves
	// every request to a signed-in identity (internal/appsession).
	session *appsession.Manager

	// gatewayURL is the Gateway's externally-reachable base URL, served to
	// the FE via GET /api/config so it can submit writes browser-direct, and
	// used server-side by resolveSubjectHats to ask the Gateway's own
	// external /v1/actor door which anchors the signed-in session carries.
	gatewayURL string

	// pgPool is the protected wellnessIdentitiesRead read-model pool; nil
	// when WELLNESS_APP_PG_DSN / REFRACTOR_PG_DSN is unset → /api/identities
	// returns a clean 502 rather than panicking.
	pgPool *pgxpool.Pool
}

// pgxBeginner is the subset of *pgxpool.Pool the protected read uses — a
// single Begin so the query path can be unit-tested with a fake transaction.
type pgxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embed guarantees web/ exists at build time; a failure here is a
		// programmer error, not a runtime condition.
		panic("wellness-app: embed web sub-fs: " + err.Error())
	}
	// The kit's own routes (login/logout/whoami/refresh) and every /api/*
	// handler live on the INNER mux, wrapped in RequireSession — /login
	// itself must be reachable with no session, which registering it on the
	// session-gated mux would break. The two schedule reads are gated the
	// same way but exempted by name (ExtraExemptPaths, main.go): the class
	// schedule is public-read.
	inner := http.NewServeMux()
	inner.Handle("/", http.FileServer(http.FS(sub)))
	inner.Handle("/shared/", http.StripPrefix("/shared/", http.FileServer(descriptorform.FS())))

	inner.HandleFunc("/api/studios", s.handleStudios)
	inner.HandleFunc("/api/sessions", s.handleSessions)
	inner.HandleFunc("/api/roster-sessions", s.handleRosterSessions)
	inner.HandleFunc("/api/instructors", s.handleInstructors)
	inner.HandleFunc("/api/bookings", s.handleBookings)
	inner.HandleFunc("/api/ledger", s.handleLedger)
	inner.HandleFunc("/api/my-residency", s.handleMyResidency)
	inner.HandleFunc("/api/members", s.handleMembers)
	inner.HandleFunc("/api/identities", s.handleIdentities)
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
