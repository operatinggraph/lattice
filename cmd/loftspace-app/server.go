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
	// return a clean 502 rather than panicking. authn verifies the read actor's
	// JWT; nil when no auth posture is configured → protected reads 401 (fail
	// closed). devSigner mints demo tokens; nil unless LOFTSPACE_APP_DEV_AUTH.
	pgPool    *pgxpool.Pool
	authn     *auth.Authenticator
	devSigner *devSigner

	// credBindings is the shared credential→identity resolution seam
	// authenticateRead consults (real-actor-write-auth-e2e-design.md §5);
	// nil when the credential-bindings bucket is unavailable — every actor
	// then reads as itself, exactly as before this seam existed.
	credBindings credentialBindingResolver
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
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/listings", s.handleListings)
	mux.HandleFunc("/api/staff/identities", s.handleStaffIdentities)
	mux.HandleFunc("/api/applications", s.handleApplications)
	mux.HandleFunc("/api/unit-applications", s.handleUnitApplications)
	mux.HandleFunc("/api/landlord/applications", s.handleLandlordApplications)
	mux.HandleFunc("/api/renewals", s.handleRenewals)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/op", s.handleOp)
	mux.HandleFunc("/api/objects", s.handleObjects)
	mux.HandleFunc("/api/objects/", s.handleObjects)
	mux.HandleFunc("/api/lease-document", s.handleLeaseDocument)
	mux.HandleFunc("/api/ledger", s.handleLedger)
	mux.HandleFunc("/api/dev-token", s.handleDevToken)
	mux.HandleFunc("/api/staff/dev-token", s.handleStaffDevToken)
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

// adminActorID returns the bare NanoID of the app's admin actor — the RLS
// session identity (lattice.actor_id) for server-side protected reads the app
// makes on its own behalf (the WildcardAnchor-holding root-equivalent
// identity, the same subject handleStaffDevToken mints for). ok is false when
// the bootstrap file was never loaded or the key is malformed.
func (s *server) adminActorID() (string, bool) {
	if s.adminActor == "" {
		return "", false
	}
	_, subject, ok := substrate.ParseVertexKey(s.adminActor)
	return subject, ok
}

// requireBody reads up to 1 MiB of the request body, the cap for the small JSON
// op payloads this app submits.
func requireBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// mustJSON marshals v, returning a hand-built error object only if marshalling
// fails (which it cannot for the small maps used here).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"internal: marshal failed"}`)
	}
	return b
}
