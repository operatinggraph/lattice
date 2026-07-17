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
//	FACET_DEV_PRIVATE_KEY_PATH  overrides the shared dev signing key path (optional)
//	FACET_DEV_PUBLIC_KEY_PATH   overrides the shared dev trust key path (optional)
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
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/edge/agent"
)

const (
	defaultHTTPAddr    = "127.0.0.1:7810"
	defaultGatewayURL  = "http://localhost:8080"
	defaultStoreDir    = "./facet-store"
	agentDrainInterval = 5 * time.Second
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
	authn, err := setupSessionAuthn(logger, signer)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	engines := newEngineManager(ctx, engineManagerDeps{
		engineConfig: engineConfig{NATSURL: natsURL, GatewayURL: gatewayURL, StoreDir: storeDir, Logger: logger},
		Signer:       signer,
	})
	defer engines.CloseAll()

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

	srv := &server{
		logger:         logger,
		gatewayURL:     gatewayURL,
		devSigner:      signer,
		authn:          authn,
		engines:        engines,
		bootIdentityID: bootIdentityID,
		loopback:       loopback,
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
// by every engine (engine.go), not just a single boot-time one.
func runAgentLoop(ctx context.Context, ag *agent.Agent, logger *slog.Logger) {
	ticker := time.NewTicker(agentDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ag.Drain(ctx); err != nil {
				logger.Warn("facet agent: drain failed, will retry", "err", err)
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
