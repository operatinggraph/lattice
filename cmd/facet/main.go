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
// Environment (mirrors cmd/edge's flat layout, plus an HTTP listener):
//
//	EDGE_STORE_PATH    path to the local bbolt store file (default: ./facet.db)
//	NATS_URL           NATS server URL (default: nats://localhost:4222)
//	EDGE_GATEWAY_URL   the Gateway's base URL intents submit through (default: http://localhost:8080)
//	EDGE_IDENTITY_ID   the identity NanoID this node mirrors (required)
//	EDGE_DEVICE_ID     this device's id (required)
//	EDGE_TOKEN         bearer JWT (Contract #11) authenticating the NATS connection
//	                   and every Gateway submit (required)
//	FACET_HTTP_ADDR    HTTP listen address (default: 127.0.0.1:7810)
//	FACET_DEV_AUTH     set to enable POST /api/claim (Fire 3, claim.go) — mints a
//	                   throwaway device credential in-process to run the real
//	                   ClaimIdentity ceremony; loopback-only, demo posture only
//	FACET_DEV_PRIVATE_KEY_PATH  overrides the shared dev signing key path (optional)
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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/edge/agent"
	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/edge/sync"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultHTTPAddr    = "127.0.0.1:7810"
	defaultGatewayURL  = "http://localhost:8080"
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
	storePath := envOrDefault("EDGE_STORE_PATH", "./facet.db")
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	gatewayURL := envOrDefault("EDGE_GATEWAY_URL", defaultGatewayURL)
	httpAddr := envOrDefault("FACET_HTTP_ADDR", defaultHTTPAddr)
	identityID := os.Getenv("EDGE_IDENTITY_ID")
	deviceID := os.Getenv("EDGE_DEVICE_ID")
	if identityID == "" || deviceID == "" {
		return errors.New("EDGE_IDENTITY_ID and EDGE_DEVICE_ID must both be set")
	}
	token := os.Getenv("EDGE_TOKEN")
	if token == "" {
		return errors.New("EDGE_TOKEN must be set")
	}

	signer, err := setupDevSigner(logger, isLoopbackHost(hostOf(httpAddr)))
	if err != nil {
		return err
	}

	st, err := store.Open(storePath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	logger.Info("local VAL store opened", "path", storePath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL: natsURL,
		// Must be the BARE device id: natsauth.go's Handle reads
		// req.ClientInformation.Name (this CONNECT option) directly as
		// deviceID and splices it into the allowed durable-consumer subject
		// as fmt.Sprintf("edge-sync-%s-%s", identityID, deviceID) —
		// PermissionsFor's exact literal, matching sync.Manager's own
		// "edge-sync-"+IdentityID+"-"+DeviceID durable name. A composite
		// "facet-<id>-<device>" string here breaks that match (permissions
		// violation on $JS.API.CONSUMER.CREATE) — the same latent mismatch
		// exists in cmd/edge/main.go's identical "edge-"+id+"-"+device Name.
		Name:          deviceID,
		MaxReconnects: -1,
		ReconnectWait: 2 * time.Second,
		Token:         token,
		// Must be "_INBOX.edge." (not "_INBOX.facet.") — natsauth's issued
		// permission set grants exactly this literal prefix regardless of
		// which app connects (internal/gateway/natsauth/natsauth.go's
		// inboxPrefix constant), keyed off the verified identity, not the
		// connecting app's name.
		InboxPrefix: "_INBOX.edge." + identityID,
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	logger.Info("connected to NATS", "natsURL", natsURL)

	fd := newFeed()
	overlayStore := overlay.New(st)

	mgr, err := sync.New(conn, st, sync.Config{
		IdentityID: identityID,
		DeviceID:   deviceID,
		// The control-plane's ActorVerifier is a separate opt-in from the
		// Gateway's (internal/controlauth.WireActorVerifierFromEnv needs
		// LATTICE_CONTROL_JWT_DEV_MODE/_KEYS_DIR on the Refractor process;
		// make up-full's dev stack doesn't set it) — until an operator
		// enables it, the control plane runs Fire 1's documented
		// self-asserted-header fallback (internal/edge/sync's own doc:
		// "no verifier configured preserves the self-asserted body"), which
		// expects the literal actor key here, not the bearer JWT (that
		// still authenticates the NATS connection itself via Token above,
		// and every Gateway submit via agent.GatewaySubmitter).
		ActorHeader: "vtx.identity." + identityID,
		Logger:      logger,
		OnChange: func(key string, deleted bool) {
			fd.publishManifestKey(overlayStore, key, deleted)
		},
		OnHydrationComplete: func(revision uint64) {
			fd.publishReady(revision)
		},
	})
	if err != nil {
		return err
	}

	submitter := &trackingSubmitter{
		inner: &agent.GatewaySubmitter{URL: gatewayURL, Token: token},
		feed:  fd,
	}
	ag := agent.New(submitter, st, overlayStore, mgr, agent.Config{
		Logger: logger,
		Conflict: func(c agent.ConflictInfo) {
			logger.Warn("facet agent: intent rejected", "requestId", c.RequestID, "keys", c.Keys)
		},
	})
	go runAgentLoop(ctx, ag, logger)

	srv := &server{
		conn:       conn,
		store:      st,
		overlay:    overlayStore,
		agent:      ag,
		feed:       fd,
		logger:     logger,
		identityID: identityID,
		gatewayURL: gatewayURL,
		devSigner:  signer,
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

	logger.Info("facet listening", "addr", httpAddr, "identityId", identityID, "deviceId", deviceID, "gatewayUrl", gatewayURL)

	httpErrCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
			return
		}
		httpErrCh <- nil
	}()

	syncErrCh := make(chan error, 1)
	go func() { syncErrCh <- mgr.Run(ctx) }()

	select {
	case <-ctx.Done():
		logger.Info("signal received; shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
		<-syncErrCh
		return nil
	case err := <-httpErrCh:
		return err
	case err := <-syncErrCh:
		return err
	}
}

// runAgentLoop periodically drains the intent queue and sweeps the
// overlay's local GC — identical cadence/shape to cmd/edge's own loop.
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
