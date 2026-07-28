// cmd/cafe-app — the Café app: a local web front-end for opening, charging,
// and settling resident house tabs over a running Lattice deployment. Three
// thin views: POS (pick a lease, open/charge/settle its tab), Front Desk (the
// house-wide list of open tabs, a staff-only surface), and Resident (a
// signed-in resident's own open tab + posted café ledger history).
//
// It is a vertical product app, distinct from Loupe (the operator tool) and a
// sibling of loftspace-app/clinic-app/wellness-app. It is SIGN-IN-FIRST: every
// request is gated on a browser session (internal/appsession). A person signs
// in at /login, and the resulting HttpOnly cookie carries the JWT that is
// simultaneously the read boundary's principal here and the actor the Gateway
// verifies on writes. The app holds no way to mint a token for anyone but the
// person signing in, so it can never act as a subject the browser merely
// named.
//
// WRITES go browser-direct to the Gateway's POST /v1/operations under the
// signed-in actor's own token (real-actor-write-auth-e2e-design.md §3.1) —
// the app never proxies a write. Most READS are plain NATS-KV lens
// projections (P5) scoped server-side by the same session identity: a
// resident sees only rows for leases whose bookerKey is their own identity,
// and a `worksAt` staffer sees the house (persona-worlds-design.md Fire W4
// §3). /api/identities is the one protected Postgres read model
// (cafeIdentitiesRead, RLS-scoped): a signed-in actor's own name, resolved
// for the "Signed in as" bar.
//
// The app's own NATS connection acts as admin behind that session, so it
// binds 127.0.0.1 only by default; a non-loopback CAFE_APP_ADDR is an
// explicit opt-in and logs a loud warning at startup.
//
// Environment:
//
//	CAFE_APP_ADDR      HTTP listen address (default: 127.0.0.1:7801)
//	NATS_URL           NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	CAFE_APP_PG_DSN    Postgres DSN for the protected cafeIdentitiesRead read
//	                   model; falls back to REFRACTOR_PG_DSN. Unset ⇒
//	                   /api/identities reports the model unconfigured.
//	CAFE_APP_DEV_AUTH  "1" enables the demo in-process minter behind /api/dev-login
//	                   (loopback bind only).
//	CAFE_APP_JWT_PUBLIC_KEY / _JWT_ISSUER  the production verify-only posture: an
//	                   external IdP's PEM public key and the issuer it is pinned to
//	                   (optionally _JWT_KID, _JWT_AUDIENCE). Nothing is minted here.
//	CAFE_APP_DEMO_PERSONAS  JSON list fencing sign-in to a curated set of seeded
//	                   identities (the hosted-demo posture).
//	CAFE_APP_PUBLIC_ORIGIN  the exact external https origin a proxied deployment is
//	                   served at (e.g. https://cafe.demo.example). Admits that origin
//	                   at the cross-origin write gate and forces the session cookie's
//	                   Secure flag; unset is the loopback posture.
//	CAFE_APP_INSTANCE  Health-KV instance id (default: auto-generated cafe-<NanoID>).
//	CAFE_APP_HEARTBEAT_EVERY  Health-KV heartbeat cadence (default: 10s).
//	CAFE_APP_GATEWAY_URL  the Gateway's base URL the FE submits writes to, browser-direct
//	                      (default: http://localhost:8080).
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
	appName   = "cafe-app"
	envPrefix = "CAFE_APP"

	defaultAddr       = "127.0.0.1:7801"
	natsRequestLimit  = 8 * time.Second
	defaultGatewayURL = "http://localhost:8080"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("cafe-app exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("CAFE_APP_ADDR", defaultAddr)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	warnIfNonLoopback(logger, addr)

	// A missing/invalid bootstrap file is NOT fatal: the UI still serves and
	// every read is scoped by the signed-in session rather than any
	// platform-derived actor. What is lost is the Health-KV bucket
	// provisioning signal, so this process cannot report its own health as
	// fully configured.
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
		Name:          "cafe-app",
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

	// The read boundary for /api/identities — the protected cafeIdentitiesRead
	// Postgres read model + the JWT-authenticated reader (mirrors
	// cmd/clinic-app's readModelDSN wiring). A missing DSN is NOT fatal (the
	// UI still serves and /api/identities returns a clean error), but a
	// configured DSN that cannot be parsed IS fatal.
	var pgPool *pgxpool.Pool
	if dsn := readModelDSN(); dsn != "" {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		pgPool = pool
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pool.Ping(pingCtx); err != nil {
			logger.Warn("protected read model pool configured but unreachable at startup; /api/identities will 502 until Postgres is reachable",
				"error", err)
		} else {
			logger.Info("protected read model pool configured")
		}
		cancel()
	} else {
		logger.Warn("CAFE_APP_PG_DSN / REFRACTOR_PG_DSN unset; /api/identities will report the protected read model is unconfigured")
	}

	// Token-revocation kill-switch (external-actor-authn-binding-design.md
	// §12.1) is additive/best-effort here: a deployment that hasn't re-run
	// bootstrap yet (bucket doesn't exist) still starts — sessions simply
	// aren't revocation-gated until the bucket is provisioned, with the short
	// token TTL as the backstop.
	var revocationChecker auth.RevocationChecker
	if conn != nil {
		if revKV, err := conn.OpenKV(context.Background(), revocation.BucketName); err != nil {
			logger.Warn("cafe-app: token-revocation bucket unavailable; revocation kill-switch disabled for reads", "error", err)
		} else {
			revocationChecker = revocation.New(revKV)
		}
	}

	gatewayURL := envOrDefault("CAFE_APP_GATEWAY_URL", defaultGatewayURL)
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
		logger.Warn("no session auth posture (set CAFE_APP_DEV_AUTH, or CAFE_APP_JWT_PUBLIC_KEY + CAFE_APP_JWT_ISSUER); every /api/* request will return 401")
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
	// No FallbackIdentityID: a café browser with no cookie is genuinely
	// anonymous. There is no single-user boot identity to inherit, and every
	// read is scoped to a specific resident or the worksAt staff hat.
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
		pgPool:          pgPool,
		authn:           authn,
		session:         session,
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

	// Contract #5 heartbeat — dependency-probing, gated on a live NATS dial.
	if conn != nil {
		instance := envOrDefault("CAFE_APP_INSTANCE", "")
		if instance == "" {
			id, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate health-kv instance id: %w", err)
			}
			instance = "cafe-" + id
		}
		reporter := healthkv.New(healthkv.Config{
			Conn:      conn,
			Bucket:    bootstrap.HealthKVBucket,
			Component: "cafe-app",
			Instance:  instance,
			Interval:  envDuration("CAFE_APP_HEARTBEAT_EVERY", 10*time.Second, logger),
			Probe:     srv.healthProbe,
			Logger:    logger,
		})
		go reporter.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("cafe-app listening", "addr", addr)
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
// loopback host: this app's own NATS connection acts as admin behind every
// session and it serves plain http, so a non-local bind puts both that
// surface and the session cookie on the wire.
func warnIfNonLoopback(logger *slog.Logger, addr string) {
	host := appsession.HostOf(addr)
	if host == "" {
		logger.Warn("could not parse CAFE_APP_ADDR host; ensure it binds a loopback address", "addr", addr)
		return
	}
	if appsession.IsLoopbackHost(host) {
		return
	}
	logger.Warn("cafe-app's own NATS connection acts as admin behind every session; binding to a non-local address exposes that surface, and the session cookie, to the network",
		"addr", addr)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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

// readModelDSN resolves the protected read model's Postgres DSN. It prefers
// the app-specific CAFE_APP_PG_DSN (which may name a non-superuser,
// SELECT-only role distinct from Refractor's projector role) and falls back
// to the shared REFRACTOR_PG_DSN. Empty when neither is set.
func readModelDSN() string {
	if v := strings.TrimSpace(os.Getenv("CAFE_APP_PG_DSN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("REFRACTOR_PG_DSN"))
}
