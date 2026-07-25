// cmd/wellness-app — the Wellness app: a local web front-end for scheduling
// and booking studio classes over a running Lattice deployment. Four thin
// views: Schedule (every upcoming session across studios, book a seat),
// Roster (one session's booked seats — a staff/instructor read), My Classes
// (the signed-in member's own bookings), and Studios (the staff surface for
// scheduling a class).
//
// It is a vertical product app, distinct from Loupe (the operator tool) and a
// sibling of loftspace-app/clinic-app/cafe-app. It is SIGN-IN-FIRST: a person
// signs in at /login, and the resulting HttpOnly cookie carries the JWT that
// is simultaneously the read boundary's principal here and the actor the
// Gateway verifies on writes. The app holds no way to mint a token for anyone
// but the person signing in, so it can never act as a subject the browser
// merely named.
//
// WRITES go browser-direct to the Gateway's POST /v1/operations under the
// signed-in actor's own token — the app never proxies a write. READS are all
// plain NATS-KV lens projections (P5) plus the shared weaver-targets
// convergence bucket — no protected Postgres read model exists for wellness,
// so this app carries no pgxpool — and the boundary over them is the session
// (persona-worlds-design.md Fire W3 §3): the class SCHEDULE is public-read
// (studios and sessions carry no person-identifying column), while every
// per-user read is gated and scoped server-side to the session's subject.
// Three hats derive from the Gateway's own /v1/actor anchors: a member (any
// verified session), a `worksAt` staffer, and an instructor bound to a
// vtx.instructor entity by `identifiedBy`.
//
// The app's own NATS connection acts as admin behind that session, so it
// binds 127.0.0.1 only by default; a non-loopback WELLNESS_APP_ADDR is an
// explicit opt-in and logs a loud warning at startup.
//
// Environment:
//
//	WELLNESS_APP_ADDR      HTTP listen address (default: 127.0.0.1:7802)
//	NATS_URL               NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH    path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	WELLNESS_APP_DEV_AUTH  "1" enables the demo in-process minter behind /api/dev-login
//	                       (loopback bind only).
//	WELLNESS_APP_JWT_PUBLIC_KEY / _JWT_ISSUER  the production verify-only posture: an
//	                       external IdP's PEM public key and the issuer it is pinned to
//	                       (optionally _JWT_KID, _JWT_AUDIENCE). Nothing is minted here.
//	WELLNESS_APP_DEMO_PERSONAS  JSON list fencing sign-in to a curated set of seeded
//	                       identities (the hosted-demo posture).
//	WELLNESS_APP_PUBLIC_ORIGIN  the exact external https origin a proxied deployment is
//	                       served at. Admits that origin at the cross-origin write gate
//	                       and forces the session cookie's Secure flag; unset is the
//	                       loopback posture.
//	WELLNESS_APP_INSTANCE  Health-KV instance id (default: auto-generated wellness-<NanoID>).
//	WELLNESS_APP_HEARTBEAT_EVERY  Health-KV heartbeat cadence (default: 10s).
//	WELLNESS_APP_GATEWAY_URL  the Gateway's base URL the FE submits writes to, browser-direct
//	                          (default: http://localhost:8080).
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
	"syscall"
	"time"

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
	appName   = "wellness-app"
	envPrefix = "WELLNESS_APP"

	defaultAddr       = "127.0.0.1:7802"
	natsRequestLimit  = 8 * time.Second
	defaultGatewayURL = "http://localhost:8080"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("wellness-app exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("WELLNESS_APP_ADDR", defaultAddr)
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
		Name:          "wellness-app",
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

	// Token-revocation kill-switch (external-actor-authn-binding-design.md
	// §12.1) is additive/best-effort here: a deployment that hasn't re-run
	// bootstrap yet (bucket doesn't exist) still starts — sessions simply
	// aren't revocation-gated until the bucket is provisioned, with the short
	// token TTL as the backstop.
	var revocationChecker auth.RevocationChecker
	if conn != nil {
		if revKV, err := conn.OpenKV(context.Background(), revocation.BucketName); err != nil {
			logger.Warn("wellness-app: token-revocation bucket unavailable; revocation kill-switch disabled for reads", "error", err)
		} else {
			revocationChecker = revocation.New(revKV)
		}
	}

	gatewayURL := envOrDefault("WELLNESS_APP_GATEWAY_URL", defaultGatewayURL)
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
		logger.Warn("no session auth posture (set WELLNESS_APP_DEV_AUTH, or WELLNESS_APP_JWT_PUBLIC_KEY + WELLNESS_APP_JWT_ISSUER); every gated /api/* request will return 401")
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
	// No FallbackIdentityID: a wellness browser with no cookie is genuinely
	// anonymous. There is no single-user boot identity to inherit — the only
	// thing it may read is the public class schedule, and every other read is
	// scoped to a specific member or to a staff/instructor hat.
	session, err := appsession.New(appsession.Config{
		AppName:          appName,
		EnvPrefix:        envPrefix,
		Logger:           logger,
		GatewayURL:       gatewayURL,
		Signer:           signer,
		Authn:            authn,
		RefreshAuthn:     refreshAuthn,
		Loopback:         loopback,
		BindHost:         bindHost,
		PublicOrigin:     publicOrigin,
		Personas:         personas,
		LoginPage:        loginPage,
		ExtraExemptPaths: publicReadPaths,
	})
	if err != nil {
		return err
	}

	srv := &server{
		conn:            conn,
		bootstrapLoaded: bootstrapLoaded,
		logger:          logger,
		natsTimeout:     natsRequestLimit,
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
		instance := envOrDefault("WELLNESS_APP_INSTANCE", "")
		if instance == "" {
			id, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate health-kv instance id: %w", err)
			}
			instance = "wellness-" + id
		}
		reporter := healthkv.New(healthkv.Config{
			Conn:      conn,
			Bucket:    bootstrap.HealthKVBucket,
			Component: "wellness-app",
			Instance:  instance,
			Interval:  envDuration("WELLNESS_APP_HEARTBEAT_EVERY", 10*time.Second, logger),
			Probe:     srv.healthProbe,
			Logger:    logger,
		})
		go reporter.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("wellness-app listening", "addr", addr)
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
		logger.Warn("could not parse WELLNESS_APP_ADDR host; ensure it binds a loopback address", "addr", addr)
		return
	}
	if appsession.IsLoopbackHost(host) {
		return
	}
	logger.Warn("wellness-app's own NATS connection acts as admin behind every session; binding to a non-local address exposes that surface, and the session cookie, to the network",
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
