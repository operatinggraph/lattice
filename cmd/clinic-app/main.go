// cmd/clinic-app — the Clinic app: a local web front-end for booking and tracking
// clinic appointments over a running Lattice deployment. A person picks who they
// are (a patient), browses providers, books an appointment, tracks their
// appointments, and a clinic-desk view shows a provider's schedule.
//
// It is a vertical product app, distinct from Loupe (the operator tool) and a
// sibling of loftspace-app. Like both it is a trusted single-identity tool for
// WRITES: it connects to NATS as the primordial admin actor and submits
// operations on the user's behalf — there is no Gateway, and no per-user authZ
// on any write. READS are mixed: most stay on the unauthenticated admin path
// (the view is patient-centric — the user selects which patient they are and
// the UI scopes its reads to that patient, but the server does not verify the
// selection). /api/my-appointments, /api/my-schedule, and /api/staff/appointments
// are the exceptions (D1.5): each reads a protected Postgres model as a
// JWT-AUTHENTICATED actor (RLS) — patient-self, provider-self, and (via the
// reserved WildcardAnchor grant, D1 design §3.4 M5) the clinic-wide staff view,
// respectively.
//
// SAFETY: this app has NO authentication and acts as admin. It binds 127.0.0.1
// only by default; a non-loopback CLINIC_APP_ADDR is an explicit opt-in and logs a
// loud warning at startup.
//
// Environment:
//
//	CLINIC_APP_ADDR      HTTP listen address (default: 127.0.0.1:7799)
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	CLINIC_APP_PG_DSN    Postgres DSN for the protected clinicAppointmentsRead read
//	                     model (D1.5); falls back to REFRACTOR_PG_DSN. Unset ⇒
//	                     /api/my-appointments reports the model unconfigured.
//	CLINIC_APP_DEV_AUTH  "1" enables the demo dev-token minter (loopback bind only).
//
// The server starts even when NATS is unreachable or the bootstrap file is
// missing: the UI is served and each /api/* call returns a JSON error the UI
// renders, never a crash.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr      = "127.0.0.1:7799"
	natsRequestLimit = 8 * time.Second
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("clinic-app exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("CLINIC_APP_ADDR", defaultAddr)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	warnIfNonLoopback(logger, addr)

	// The primordial admin actor. A missing/invalid bootstrap file is NOT fatal:
	// the UI still serves and /api/* handlers report the unconfigured actor as a
	// clean JSON error. Without an admin actor, ops that submit (book / cancel /
	// create) report an error; pure reads are unaffected.
	var adminActor string
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		logger.Warn("bootstrap file not loaded; booking and status changes will report an error until it is present",
			"path", bootstrapJSONPath, "error", err)
	} else {
		adminActor = bootstrap.BootstrapIdentityKey
		logger.Info("admin actor loaded", "actor", adminActor)
	}

	// A failed dial is NOT fatal: substrate reconnects in the background and each
	// handler bounds its own request so a still-down NATS surfaces as a JSON
	// error rather than a hang.
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "clinic-app",
		MaxReconnects: -1,
		ReconnectWait: 2 * time.Second,
	})
	if err != nil {
		logger.Warn("NATS connect failed at startup; serving UI, /api/* will report errors until NATS is reachable",
			"natsURL", natsURL, "error", err)
	} else {
		logger.Info("connected to NATS", "natsURL", natsURL)
		defer conn.Close()
	}

	// The read boundary (D1.5) — the protected clinicAppointmentsRead Postgres
	// read model + the JWT-authenticated reader. Both dependencies are optional
	// at startup: a missing DSN or auth posture is NOT fatal (the UI still
	// serves and /api/my-appointments returns a clean error), but a configured
	// DSN that cannot be parsed IS fatal (a misconfiguration the operator must
	// fix).
	var pgPool *pgxpool.Pool
	if dsn := readModelDSN(); dsn != "" {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		pgPool = pool
		// pgxpool.New is lazy (no connection yet); ping so a dead/unauthorized
		// Postgres surfaces at boot rather than as a per-request 502. Non-fatal:
		// the pool reconnects lazily if Postgres comes up later.
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pool.Ping(pingCtx); err != nil {
			logger.Warn("protected read model pool configured but unreachable at startup; every protected endpoint (/api/my-appointments, /api/my-schedule, /api/staff/appointments, /api/my-visit-series, /api/staff/visit-series) will 502 until Postgres is reachable",
				"error", err)
		} else {
			logger.Info("protected read model pool configured")
		}
		cancel()
	} else {
		logger.Warn("CLINIC_APP_PG_DSN / REFRACTOR_PG_DSN unset; every protected endpoint will report the protected read model is unconfigured")
	}

	authn, signer, err := setupReadAuth(logger, isLoopbackHost(hostOf(addr)))
	if err != nil {
		return err
	}
	if authn == nil {
		logger.Warn("read boundary has no auth posture (set CLINIC_APP_DEV_AUTH or CLINIC_APP_JWT_PUBLIC_KEY); /api/my-appointments will return 401")
	}

	srv := &server{
		conn:        conn,
		adminActor:  adminActor,
		logger:      logger,
		natsTimeout: natsRequestLimit,
		pgPool:      pgPool,
		authn:       authn,
		devSigner:   signer,
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("clinic-app listening", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received; shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// warnIfNonLoopback logs a loud warning when addr binds anything other than a
// loopback host: this app is auth-less and acts as admin, so a non-local bind
// exposes admin control to the network.
func warnIfNonLoopback(logger *slog.Logger, addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		logger.Warn("could not parse CLINIC_APP_ADDR host; ensure it binds a loopback address", "addr", addr, "error", err)
		return
	}
	if isLoopbackHost(host) {
		return
	}
	logger.Warn("clinic-app has no auth and acts as admin; binding to a non-local address exposes admin control to the network",
		"addr", addr)
}

// isLoopbackHost reports whether host is a loopback bind. An empty host (the
// bare ":7799" form) means all interfaces and is NOT loopback.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// hostOf returns the host portion of a listen address, or "" when it cannot be
// parsed (treated as non-loopback by isLoopbackHost — fail safe).
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readModelDSN resolves the protected read model's Postgres DSN. It prefers the
// app-specific CLINIC_APP_PG_DSN (which may name a non-superuser, SELECT-only
// role distinct from Refractor's projector role) and falls back to the shared
// REFRACTOR_PG_DSN. Empty when neither is set.
func readModelDSN() string {
	if v := strings.TrimSpace(os.Getenv("CLINIC_APP_PG_DSN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("REFRACTOR_PG_DSN"))
}
