// cmd/loftspace-app — the LoftSpace applicant app: a local web front-end for a
// person to browse leasable units, apply, track their application, complete
// their tasks, and upload documents over a running Lattice deployment.
//
// It is a vertical product app, distinct from Loupe (the operator tool).
// Operation submits (apply, sign, list, decide, ...) go browser-direct to the
// Gateway's POST /v1/operations with the signed-in actor's own Bearer token
// (real-actor-write-auth-e2e-design.md §3.1) — this server no longer stamps a
// fixed admin actor on them. It still connects to NATS as the primordial admin
// actor for its remaining direct handlers (object upload/download, lease
// document generation) that predate the operation write path.
// READS are mixed: several protected Postgres read models (D1.5) require a
// JWT-authenticated actor — including /api/staff/identities, the picker the
// user reads to select which identity they are, which uses the system-wide
// staff/wildcard grant so it works before an applicant has been selected.
// The view is applicant-centric: the user selects which identity they are and
// the UI scopes its reads and writes to that applicant.
//
// SAFETY: this app has NO authentication and acts as admin. It binds 127.0.0.1
// only by default; a non-loopback LOFTSPACE_APP_ADDR is an explicit opt-in and
// logs a loud warning at startup.
//
// Environment:
//
//	LOFTSPACE_APP_ADDR            HTTP listen address (default: 127.0.0.1:7788)
//	NATS_URL                      NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH           path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	LOFTSPACE_APP_INSTANCE        Health-KV instance id (default: auto-generated loft-<NanoID>)
//	LOFTSPACE_APP_HEARTBEAT_EVERY Health-KV heartbeat cadence (default: 10s)
//	LOFTSPACE_APP_GATEWAY_URL     the Gateway's base URL the FE submits writes to, browser-direct
//	                              (default: http://localhost:8080; real-actor-write-auth-e2e-design.md §3.1)
//
// The server starts even when NATS is unreachable or the bootstrap file is
// missing: the UI is served and each /api/* call returns a JSON error the UI
// renders, never a crash.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/healthkv"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr      = "127.0.0.1:7788"
	natsRequestLimit = 8 * time.Second
	// defaultUploadCap bounds a single document upload (OBJECTS_MAX_UPLOAD_BYTES).
	defaultUploadCap  = 25 << 20 // 25 MiB
	defaultGatewayURL = "http://localhost:8080"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("loftspace-app exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("LOFTSPACE_APP_ADDR", defaultAddr)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	warnIfNonLoopback(logger, addr)

	// The primordial admin actor. A missing/invalid bootstrap file is NOT fatal:
	// the UI still serves and /api/* handlers report the unconfigured actor as a
	// clean JSON error. Without an admin actor, ops that submit (apply / sign)
	// report an error; pure reads are unaffected.
	var adminActor string
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		logger.Warn("bootstrap file not loaded; applying and signing will report an error until it is present",
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
		Name:          "loftspace-app",
		MaxReconnects: -1,
		ReconnectWait: 2 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		logger.Warn("NATS connect failed at startup; serving UI, /api/* will report errors until NATS is reachable",
			"natsURL", natsURL, "error", err)
	} else {
		logger.Info("connected to NATS", "natsURL", natsURL)
		defer conn.Close()
	}

	uploadCap := int64(defaultUploadCap)
	if v := os.Getenv("OBJECTS_MAX_UPLOAD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			uploadCap = n
		} else {
			logger.Warn("ignoring invalid OBJECTS_MAX_UPLOAD_BYTES; using default",
				"value", v, "default", defaultUploadCap)
		}
	}

	// The read boundary (D1.3 Fire 3) — the protected lease-applications Postgres
	// read model + the JWT-authenticated reader. Both dependencies are optional at
	// startup: a missing DSN or auth posture is NOT fatal (the UI still serves and
	// /api/applications returns a clean error), but a configured DSN that cannot
	// be parsed IS fatal (a misconfiguration the operator must fix).
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
			logger.Warn("protected read model pool configured but unreachable at startup; /api/applications will 502 until Postgres is reachable",
				"error", err)
		} else {
			logger.Info("protected read model pool configured")
		}
		cancel()
	} else {
		logger.Warn("LOFTSPACE_APP_PG_DSN / REFRACTOR_PG_DSN unset; /api/applications will report the protected read model is unconfigured")
	}

	authn, signer, err := setupReadAuth(logger, isLoopbackHost(hostOf(addr)))
	if err != nil {
		return err
	}
	if authn == nil {
		logger.Warn("read boundary has no auth posture (set LOFTSPACE_APP_DEV_AUTH or LOFTSPACE_APP_JWT_PUBLIC_KEY); /api/applications will return 401")
	}

	srv := &server{
		conn:        conn,
		adminActor:  adminActor,
		logger:      logger,
		natsTimeout: natsRequestLimit,
		uploadCap:   uploadCap,
		pgPool:      pgPool,
		authn:       authn,
		devSigner:   signer,
		gatewayURL:  envOrDefault("LOFTSPACE_APP_GATEWAY_URL", defaultGatewayURL),
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

	// Contract #5 heartbeat — dependency-probing, not a static liveness ping
	// (see health.go). Gated on a live NATS dial, mirroring object-store-manager;
	// an absent card on a NATS-down boot is itself an operator signal.
	if conn != nil {
		instance := envOrDefault("LOFTSPACE_APP_INSTANCE", "")
		if instance == "" {
			id, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate health-kv instance id: %w", err)
			}
			instance = "loft-" + id
		}
		reporter := healthkv.New(healthkv.Config{
			Conn:      conn,
			Bucket:    bootstrap.HealthKVBucket,
			Component: "loftspace-app",
			Instance:  instance,
			Interval:  envDuration("LOFTSPACE_APP_HEARTBEAT_EVERY", 10*time.Second, logger),
			Probe:     srv.healthProbe,
			Logger:    logger,
		})
		go reporter.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("loftspace-app listening", "addr", addr)
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
		logger.Warn("could not parse LOFTSPACE_APP_ADDR host; ensure it binds a loopback address", "addr", addr, "error", err)
		return
	}
	if isLoopbackHost(host) {
		return
	}
	logger.Warn("loftspace-app has no auth and acts as admin; binding to a non-local address exposes admin control to the network",
		"addr", addr)
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

// isLoopbackHost reports whether host is a loopback bind. An empty host (the
// bare ":7788" form) means all interfaces and is NOT loopback.
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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readModelDSN resolves the protected read model's Postgres DSN. It prefers the
// app-specific LOFTSPACE_APP_PG_DSN (which may name a non-superuser, SELECT-only
// role distinct from Refractor's projector role) and falls back to the shared
// REFRACTOR_PG_DSN. Empty when neither is set.
func readModelDSN() string {
	if v := strings.TrimSpace(os.Getenv("LOFTSPACE_APP_PG_DSN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("REFRACTOR_PG_DSN"))
}

func envDuration(key string, def time.Duration, logger *slog.Logger) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logger.Warn("ignoring invalid duration env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return d
}
