// cmd/facet — the Edge showcase app ("Facet"): a discovery-driven personal
// client whose entire UI is generated at runtime from the edge-manifest
// Personal Lens rows (manifest.me/svc/op/task/inst) and a fixed descriptor
// vocabulary (facet-app-ux.md, edge-showcase-app-design.md). Fire 2 / Stage 0
// (design §5): the Go host embeds internal/edge directly (same wiring as
// cmd/edge — EDGE.3's real per-identity JWT posture is already live, so
// Facet gets Gateway-verified writes and subscribe-ACL confinement for
// free, not a "trusted posture" placeholder) and serves a PWA renderer over
// a localhost HTTP/SSE feed. The browser never talks NATS directly (that is
// Stage 2 / Fire 4) — it only ever calls this process's own HTTP surface.
//
// Inc 2 (design §7.2): Facet is no longer per-process single-tenant. Each
// signed-in identity gets its own engine (engine.go), multiplexed by
// engineManager (enginemanager.go) and selected per-request by a session
// cookie (session.go) — "same binary, different identity, different app"
// (design §1) now has a runtime delivery vehicle, not just the claim
// ceremony's one-shot unclaimed→claimed transition.
//
// Environment (mirrors cmd/edge's flat layout, plus an HTTP listener):
//
//	FACET_STORE_DIR    directory holding one bbolt store file per signed-in
//	                   identity (<dir>/<identityId>.db; default: ./facet-store)
//	NATS_URL           NATS server URL (default: nats://localhost:4222)
//	EDGE_GATEWAY_URL   the Gateway's base URL intents submit through (default: http://localhost:8080)
//	EDGE_IDENTITY_ID   OPTIONAL boot-time single-user fallback identity — seeds
//	                   one engine at startup from an externally-minted
//	                   credential (see EDGE_DEVICE_ID/EDGE_TOKEN), reachable
//	                   with no login at all. Requires EDGE_DEVICE_ID + EDGE_TOKEN.
//	EDGE_DEVICE_ID     this device's id, required alongside EDGE_IDENTITY_ID
//	EDGE_TOKEN         bearer JWT (Contract #11) for the boot fallback identity,
//	                   required alongside EDGE_IDENTITY_ID
//	FACET_HTTP_ADDR    HTTP listen address (default: 127.0.0.1:7810)
//	FACET_DEV_AUTH     set to enable POST /api/claim + the /login session flow
//	                   (session.go, claim.go) — mints demo JWTs in-process for
//	                   any caller-named identity; loopback-only, demo posture only
//	FACET_DEMO_PERSONAS  OPTIONAL JSON array [{"id","label","sub"}] of curated
//	                   sign-in personas (deploy/demo): the login page offers
//	                   exactly these as one-tap cards, /api/dev-login refuses
//	                   every other subject, and /api/claim is disabled
//	FACET_DEV_PRIVATE_KEY_PATH  overrides the shared dev signing key path (optional)
//	FACET_DEV_PUBLIC_KEY_PATH   overrides the shared dev trust key path (optional)
//	FACET_PG_DSN       OPTIONAL Postgres DSN for the identityCredentialsRead
//	                   Protected read model (credentials.go, Inc 3's "manage
//	                   sign-in methods" — mirrors cmd/loftspace-app's
//	                   LOFTSPACE_APP_PG_DSN). Unset: GET /api/credentials
//	                   reports the read model as unconfigured; nothing else
//	                   in Facet depends on it.
//
// No Health-KV reporting: EDGE.3's per-identity NATS connection is confined
// by natsauth's issued permission set to exactly `lattice.sync.user.<U>` +
// its own `_INBOX.edge.<U>.>` + the personal.* control RPCs (internal/
// gateway/natsauth/natsauth.go) — publishing to health-kv is a permissions
// violation, not a missing grant to request; cmd/edge itself has never
// reported health for the same structural reason.
//
// Logs to stderr in slog text format. Blocks until SIGINT/SIGTERM.
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

	"github.com/nats-io/nats.go"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/operatinggraph/lattice/internal/edge/agent"
)

const (
	defaultHTTPAddr    = "127.0.0.1:7810"
	defaultGatewayURL  = "http://localhost:8080"
	defaultStoreDir    = "./facet-store"
	agentDrainInterval = 5 * time.Second

	// Browser-native mode (FACET_BROWSER_ENGINE, EDGE.5 W4 inc 4) asset
	// locations. wasmDir is the build-edge-wasm output; shellDir is the
	// in-tree JS transport shell; wsURL is the natsperm WebsocketPort listener.
	defaultEdgeWasmDir  = "bin/edge-wasm"
	defaultEdgeShellDir = "internal/edge/browser/shell"
	defaultEdgeWSURL    = "ws://127.0.0.1:9222"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("facet exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	storeDir := envOrDefault("FACET_STORE_DIR", defaultStoreDir)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	gatewayURL := envOrDefault("EDGE_GATEWAY_URL", defaultGatewayURL)
	httpAddr := envOrDefault("FACET_HTTP_ADDR", defaultHTTPAddr)

	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("create store dir %q: %w", storeDir, err)
	}

	loopback := isLoopbackHost(hostOf(httpAddr))
	signer, err := setupDevSigner(logger, loopback)
	if err != nil {
		return err
	}
	authn, refreshAuthn, err := setupSessionAuthn(logger, signer)
	if err != nil {
		return err
	}
	personas, err := parseDemoPersonas(os.Getenv("FACET_DEMO_PERSONAS"))
	if err != nil {
		return err
	}
	if len(personas) > 0 {
		logger.Info("demo-persona posture enabled: login is fenced to the listed personas and /api/claim is disabled", "personas", len(personas))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	engines := newEngineManager(ctx, engineManagerDeps{
		engineConfig: engineConfig{NATSURL: natsURL, GatewayURL: gatewayURL, StoreDir: storeDir, Logger: logger},
		Signer:       signer,
	})
	defer engines.CloseAll()

	// identityCredentialsRead read boundary (Inc 3, mirrors cmd/loftspace-app/
	// main.go's pgPool wiring): optional at startup, same as there — a
	// missing/unreachable DSN degrades GET /api/credentials to a clean
	// error rather than failing the whole process.
	var pgPool *pgxpool.Pool
	if dsn := strings.TrimSpace(os.Getenv("FACET_PG_DSN")); dsn != "" {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		pgPool = pool
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pool.Ping(pingCtx); err != nil {
			logger.Warn("identityCredentialsRead pool configured but unreachable at startup; /api/credentials will 502 until Postgres is reachable", "error", err)
		} else {
			logger.Info("identityCredentialsRead pool configured")
		}
		cancel()
	} else {
		logger.Warn("FACET_PG_DSN unset; /api/credentials will report the protected read model is unconfigured")
	}

	bootIdentityID := os.Getenv("EDGE_IDENTITY_ID")
	if bootIdentityID != "" {
		deviceID := os.Getenv("EDGE_DEVICE_ID")
		token := os.Getenv("EDGE_TOKEN")
		if deviceID == "" || token == "" {
			return errors.New("EDGE_DEVICE_ID and EDGE_TOKEN must both be set alongside EDGE_IDENTITY_ID")
		}
		if err := engines.Seed(bootIdentityID, deviceID, token); err != nil {
			return fmt.Errorf("seed boot identity engine: %w", err)
		}
		logger.Info("boot identity engine seeded (single-user fallback, no login required)", "identityId", bootIdentityID, "deviceId", deviceID)
	}

	var browserEngine *browserEngineConfig
	if isTruthy(os.Getenv("FACET_BROWSER_ENGINE")) {
		browserEngine = &browserEngineConfig{
			wasmDir:  envOrDefault("FACET_EDGE_WASM_DIR", defaultEdgeWasmDir),
			shellDir: envOrDefault("FACET_EDGE_SHELL_DIR", defaultEdgeShellDir),
			wsURL:    envOrDefault("EDGE_WS_URL", defaultEdgeWSURL),
		}
		logger.Info("facet browser-native serving mode enabled (EDGE.5 W4): the browser runs the engine in-page over WebSocket, no local engine binary",
			"wasmDir", browserEngine.wasmDir, "shellDir", browserEngine.shellDir, "wsUrl", browserEngine.wsURL)
	}

	srv := &server{
		logger:         logger,
		gatewayURL:     gatewayURL,
		devSigner:      signer,
		authn:          authn,
		refreshAuthn:   refreshAuthn,
		engines:        engines,
		bootIdentityID: bootIdentityID,
		loopback:       loopback,
		pgPool:         pgPool,
		browserEngine:  browserEngine,
		bootToken:      os.Getenv("EDGE_TOKEN"),
		personas:       personas,
	}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// SSE connections are long-lived; WriteTimeout must not cut them off.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("facet listening", "addr", httpAddr, "gatewayUrl", gatewayURL, "bootIdentityId", bootIdentityID, "devAuth", signer != nil)

	httpErrCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
			return
		}
		httpErrCh <- nil
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
	case err := <-httpErrCh:
		return err
	}
}

// runAgentLoop periodically drains the intent queue and sweeps the
// overlay's local GC — identical cadence/shape to cmd/edge's own loop. Used
// by every engine (engine.go), not just a single boot-time one. fd receives
// the §4.4 revocation signal: this loop is the ONLY place a dead credential
// is ever observed, since /api/enqueue returns before the Gateway is
// contacted.
func runAgentLoop(ctx context.Context, ag *agent.Agent, fd *feed, logger *slog.Logger) {
	ticker := time.NewTicker(agentDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ag.Drain(ctx); err != nil {
				// A refused credential is permanent for this engine — the
				// intents stay queued (a re-login drains them), but the
				// browser must stop waiting and be offered the sign-out
				// flow. Every other drain error is transient: keep retrying
				// silently, exactly as before.
				if errors.Is(err, agent.ErrCredentialRejected) {
					logger.Warn("facet agent: gateway refused this identity's credential; signalling sign-out", "err", err)
					fd.publishRevoked("Your session is no longer valid. Sign in again to continue.")
				} else {
					logger.Warn("facet agent: drain failed, will retry", "err", err)
				}
			}
			if _, err := ag.GC(); err != nil {
				logger.Warn("facet agent: GC failed", "err", err)
			}
		}
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
