// cmd/loftspace-app — the LoftSpace app: a local web front-end for a person to
// browse leasable units, apply, track their application, complete their tasks,
// and upload documents over a running Lattice deployment — and, for whoever
// manages those units, to work the landlord console.
//
// It is a vertical product app, distinct from Loupe (the operator tool) and a
// sibling of clinic-app. It is SIGN-IN-FIRST: every request is gated on a
// browser session (internal/appsession). A person signs in at /login, and the
// resulting HttpOnly cookie carries the JWT that is simultaneously the RLS
// principal for reads here and the actor the Gateway verifies on writes. The
// app holds no way to mint a token for anyone but the person signing in, so it
// can never act as a subject the browser merely named.
//
// WRITES go browser-direct to the Gateway's POST /v1/operations under the
// signed-in actor's own token (real-actor-write-auth-e2e-design.md §3.1) — the
// app never proxies a write. READS are served here from protected Postgres
// models under RLS, scoped by that same session identity: /api/applications,
// /api/tasks and /api/renewals answer for the caller alone, while
// /api/landlord/applications, /api/unit-applications and /api/portfolio-pulse
// answer for the units the caller manages and return nothing to anyone else.
// What a session may see is therefore decided by the grant table, never by this
// app.
//
// The app's own NATS connection acts as admin behind that session, so it binds
// 127.0.0.1 only by default; a non-loopback LOFTSPACE_APP_ADDR is an explicit
// opt-in and logs a loud warning at startup.
//
// Environment:
//
//	LOFTSPACE_APP_ADDR            HTTP listen address (default: 127.0.0.1:7788)
//	NATS_URL                      NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH           path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	LOFTSPACE_APP_PG_DSN          Postgres DSN for the protected read models (D1.3);
//	                              falls back to REFRACTOR_PG_DSN. Unset ⇒ protected reads 502.
//	LOFTSPACE_APP_DEV_AUTH        "1" enables the demo in-process minter behind /api/dev-login
//	                              (loopback bind only).
//	LOFTSPACE_APP_JWT_PUBLIC_KEY / _JWT_ISSUER  the production verify-only posture: an
//	                              external IdP's PEM public key and the issuer it is pinned to
//	                              (optionally _JWT_KID, _JWT_AUDIENCE). Nothing is minted here.
//	LOFTSPACE_APP_DEMO_PERSONAS   JSON list fencing sign-in to a curated set of seeded
//	                              identities (the hosted-demo posture).
//	LOFTSPACE_APP_PUBLIC_ORIGIN   the exact external https origin a proxied deployment is
//	                              served at (e.g. https://loftspace.demo.example). Admits that
//	                              origin at the cross-origin write gate and forces the session
//	                              cookie's Secure flag; unset is the loopback posture.
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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// appName names the app in logs and derives its session cookie's name;
	// envPrefix is the prefix every one of its env vars carries.
	appName   = "loftspace-app"
	envPrefix = "LOFTSPACE_APP"

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

	// A missing/invalid bootstrap file is NOT fatal: the UI still serves and the
	// read handlers are unaffected, since every read is scoped by the signed-in
	// session and every write goes browser-direct to the Gateway. What is lost
	// is the platform-derived Health-KV bucket name, so this process cannot
	// report its own health.
	bootstrapLoaded := true
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		bootstrapLoaded = false
		logger.Warn("bootstrap file not loaded; this process cannot publish its health until it is present",
			"path", bootstrapJSONPath, "error", err)
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

	// Token-revocation kill-switch (external-actor-authn-binding-design.md
	// §12.1) is additive/best-effort here, mirroring the credential-bindings
	// wiring just below: a deployment that hasn't re-run bootstrap yet (bucket
	// doesn't exist) still starts — reads simply aren't revocation-gated until
	// the bucket is provisioned, with the short JWT TTL as the backstop.
	var revocationChecker auth.RevocationChecker
	if conn == nil {
		logger.Warn("loftspace-app: NATS is down, so the token-revocation bucket cannot be opened; revocation kill-switch disabled for reads")
	} else if revKV, err := conn.OpenKV(context.Background(), revocation.BucketName); err != nil {
		logger.Warn("loftspace-app: token-revocation bucket unavailable; revocation kill-switch disabled for reads", "error", err)
	} else {
		revocationChecker = revocation.New(revKV)
	}

	gatewayURL := envOrDefault("LOFTSPACE_APP_GATEWAY_URL", defaultGatewayURL)
	bindHost := appsession.HostOf(addr)
	loopback := appsession.IsLoopbackHost(bindHost)
	publicOrigin, err := appsession.ParsePublicOrigin(envPrefix+"_PUBLIC_ORIGIN", os.Getenv(envPrefix+"_PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	signer, err := appsession.NewDevSigner(logger, envPrefix, loopback)
	if err != nil {
		return err
	}
	authn, refreshAuthn, err := appsession.NewAuthenticators(logger, envPrefix, signer, revocationChecker)
	if err != nil {
		return err
	}
	if authn == nil {
		logger.Warn("no session auth posture (set LOFTSPACE_APP_DEV_AUTH, or LOFTSPACE_APP_JWT_PUBLIC_KEY + LOFTSPACE_APP_JWT_ISSUER); every /api/* request will return 401")
	}
	personas, err := appsession.ParsePersonas(envPrefix+"_DEMO_PERSONAS", os.Getenv(envPrefix+"_DEMO_PERSONAS"))
	if err != nil {
		return err
	}
	if len(personas) > 0 {
		logger.Info("demo-persona posture enabled: login is fenced to the listed personas", "personas", len(personas))
	}
	loginPage, err := webFS.ReadFile("web/login.html")
	if err != nil {
		return fmt.Errorf("read embedded login page: %w", err)
	}
	// No FallbackIdentityID: a LoftSpace browser with no cookie is genuinely
	// anonymous. There is no single-user boot identity to inherit, and every
	// protected read is anchored to a specific applicant or managing landlord.
	session, err := appsession.New(appsession.Config{
		AppName:      appName,
		EnvPrefix:    envPrefix,
		Logger:       logger,
		GatewayURL:   gatewayURL,
		Signer:       signer,
		Authn:        authn,
		RefreshAuthn: refreshAuthn,
		Loopback:     loopback,
		BindHost:     bindHost,
		PublicOrigin: publicOrigin,
		Personas:     personas,
		LoginPage:    loginPage,
	})
	if err != nil {
		return err
	}

	srv := &server{
		conn:            conn,
		bootstrapLoaded: bootstrapLoaded,
		logger:          logger,
		natsTimeout:     natsRequestLimit,
		uploadCap:       uploadCap,
		pgPool:          pgPool,
		authn:           authn,
		session:         session,
		signer:          signer,
		gatewayURL:      gatewayURL,
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
// loopback host: the app's own NATS connection acts as admin behind every
// session and it serves plain http, so a non-local bind puts both that surface
// and the session cookie on the wire.
func warnIfNonLoopback(logger *slog.Logger, addr string) {
	host := appsession.HostOf(addr)
	if host == "" {
		logger.Warn("could not parse LOFTSPACE_APP_ADDR host; ensure it binds a loopback address", "addr", addr)
		return
	}
	if appsession.IsLoopbackHost(host) {
		return
	}
	logger.Warn("loftspace-app's own NATS connection acts as admin behind every session; binding to a non-local address exposes that surface, and the session cookie, to the network",
		"addr", addr)
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
