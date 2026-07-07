// cmd/loupe — Loupe, an internal trusted view-and-control web app over a
// running Lattice deployment.
//
// Loupe connects to NATS as the primordial admin actor and serves a local web
// UI plus a JSON API: browse Core KV, observe Health, drive the Refractor /
// Weaver / Loom control planes, list packages, and submit operations. The
// browser is a thin view; this Go server does all NATS I/O.
//
// SAFETY: the console requires a verified operator Bearer JWT for every
// request (readauth.go's requireOperator) — no valid token, no console. It
// binds 127.0.0.1 only by default. A non-loopback LOUPE_ADDR is an explicit
// opt-in and logs a loud warning at startup, and refuses the loopback-only
// dev-auth posture (LOUPE_DEV_AUTH requires a real IdP instead, via
// LOUPE_JWT_PUBLIC_KEY, on a non-loopback bind).
//
// Environment:
//
//	LOUPE_ADDR                 HTTP listen address (default: 127.0.0.1:7777)
//	NATS_URL                   NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH        path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	LOUPE_PG_DSN               read-only Postgres DSN for the lens-contents seam
//	                           (unset: postgres-target lens contents render pg-pending)
//	LOUPE_DEV_AUTH             loopback-only demo login: mints operator tokens via
//	                           POST /api/operator/dev-token, signed with the shared
//	                           dev key the Gateway/vertical apps also trust
//	LOUPE_JWT_PUBLIC_KEY       PEM public key of a real operator IdP (production posture);
//	                           LOUPE_JWT_ISSUER / LOUPE_JWT_AUDIENCE / LOUPE_JWT_KID refine it
//	LOUPE_GATEWAY_URL          the Gateway's base URL op-submissions relay the operator's
//	                           own Bearer token to (default: http://localhost:8080)
//
// Logs to stderr in slog text format. The server starts even when NATS is
// unreachable or the bootstrap file is missing: the UI is served and each
// /api/* call returns a JSON error the UI renders, never a crash. Neither
// LOUPE_DEV_AUTH nor LOUPE_JWT_PUBLIC_KEY set means no operator can log in —
// every request 401s until one is configured.
package main

import (
	"context"
	"errors"
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
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr       = "127.0.0.1:7777"
	natsRequestLimit  = 8 * time.Second
	defaultGatewayURL = "http://localhost:8080"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("loupe exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("LOUPE_ADDR", defaultAddr)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	warnIfNonLoopback(logger, addr)

	// Load the primordial admin actor. A missing/invalid bootstrap file is NOT
	// fatal: the UI still serves and /api/* handlers report the unconfigured
	// actor as a clean JSON error. The server starting without an admin actor
	// only disables ops/control that need it; reads are unaffected.
	var adminActor string
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		logger.Warn("bootstrap file not loaded; ops and control requiring the admin actor will report an error until it is present",
			"path", bootstrapJSONPath, "error", err)
	} else {
		adminActor = bootstrap.BootstrapIdentityKey
		logger.Info("admin actor loaded", "actor", adminActor)
	}

	// Connect to NATS. A failed dial is NOT fatal: substrate reconnects in the
	// background (MaxReconnects -1), and each handler bounds its own request so
	// a still-down NATS surfaces as a JSON error rather than a hang.
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "loupe",
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

	// The lens-contents read seam. A bad DSN does not fail startup — it is
	// remembered so postgres lens contents surface an error (not the friendly
	// pg-pending state); a good DSN with a down Postgres surfaces per-request
	// (pgxpool dials lazily). Logging the parse error is safe: pgx redacts
	// the password from its connstring errors.
	var pgPool *pgxpool.Pool
	var pgDSNInvalid bool
	if dsn := os.Getenv("LOUPE_PG_DSN"); dsn != "" {
		if pgPool, err = newLoupePGPool(dsn); err != nil {
			logger.Warn("LOUPE_PG_DSN invalid; postgres lens contents will report an error until it is fixed", "error", err)
			pgPool, pgDSNInvalid = nil, true
		} else {
			logger.Info("postgres read seam configured")
			defer pgPool.Close()
		}
	}

	bindHost, _, err := net.SplitHostPort(addr)
	if err != nil {
		bindHost = ""
	}

	// operatorActorKey is stamped as the Lattice-Actor header on every outbound
	// control-plane request (control-plane-capability-authz-design.md §3.6 —
	// "Loupe is the trusted single-identity console; its identity is now
	// explicit on the wire and granted, instead of anonymous"). Falls back to
	// adminActor (today's only configured identity) until the design's Fire 1b
	// seeds a dedicated ordinary `operator` identity — the fallback carries no
	// enforcement risk yet since Fire 1a ships no capability decision.
	operatorActorKey := envOrDefault("LOUPE_OPERATOR_ACTOR_KEY", adminActor)
	if operatorActorKey == "" {
		logger.Warn("no operator actor configured (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded); control-plane requests will carry no Lattice-Actor header")
	}
	// operatorActorToken carries a signed actor JWT (Fire 2 verified-actor
	// mode, control-plane-capability-authz-design.md §3.6/Fire 2) and, when
	// set, is stamped in place of operatorActorKey. Empty (the default)
	// keeps Fire 1's self-asserted key — mint one with
	// `gateway dev-token -sub <identityNanoID>` once the control-plane
	// servers are running with LATTICE_CONTROL_JWT_* configured.
	operatorActorToken := os.Getenv("LOUPE_OPERATOR_ACTOR_TOKEN")

	authn, signer, err := setupOperatorAuth(logger, isLoopbackHost(bindHost))
	if err != nil {
		return err
	}
	if authn == nil {
		logger.Warn("no operator auth posture configured (set LOUPE_DEV_AUTH or LOUPE_JWT_PUBLIC_KEY); every request will 401")
	}

	srv := &server{
		conn:               conn,
		adminActor:         adminActor,
		operatorActorKey:   operatorActorKey,
		operatorActorToken: operatorActorToken,
		logger:             logger,
		natsTimeout:        natsRequestLimit,
		uploadCap:          uploadCap,
		pg:                 pgPool,
		pgDSNInvalid:       pgDSNInvalid,
		bindHost:           bindHost,
		authn:              authn,
		devSigner:          signer,
		gatewayURL:         envOrDefault("LOUPE_GATEWAY_URL", defaultGatewayURL),
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.requireOperator(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout bounds a slow-reading client holding an object-byte
		// stream open; sized generously so a legitimate large download on a slow
		// link still completes. IdleTimeout reclaims idle keep-alive conns.
		WriteTimeout: 15 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("loupe listening", "addr", addr)
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

// warnIfNonLoopback logs a loud warning when addr's host resolves to anything
// other than loopback. A non-local bind requires a real operator IdP
// (LOUPE_JWT_PUBLIC_KEY) — LOUPE_DEV_AUTH refuses to enable off loopback. A
// bare ":<port>" (all interfaces) and any explicit non-loopback host trip the
// warning.
func warnIfNonLoopback(logger *slog.Logger, addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Unparseable addr — let ListenAndServe surface the real error; warn so
		// a misformatted bind that happens to be wide-open isn't silent.
		logger.Warn("could not parse LOUPE_ADDR host; ensure it binds a loopback address", "addr", addr, "error", err)
		return
	}
	if isLoopbackHost(host) {
		return
	}
	logger.Warn("Loupe is binding to a non-local address; a real operator IdP (LOUPE_JWT_PUBLIC_KEY) is required — LOUPE_DEV_AUTH refuses to enable off loopback",
		"addr", addr)
}

// isLoopbackHost reports whether host is a loopback bind. An empty host (the
// bare ":7777" form) means all interfaces and is NOT loopback. A hostname that
// is not a literal IP is treated as non-loopback unless it is "localhost".
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
