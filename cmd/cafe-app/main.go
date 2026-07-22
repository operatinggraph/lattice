// cmd/cafe-app — the Café app: a local web front-end for opening, charging,
// and settling resident house tabs over a running Lattice deployment. Three
// thin views: POS (pick a lease, open/charge/settle its tab), Front Desk (the
// clinic-wide list of open tabs), and Resident (a signed-in resident's own
// open tab + posted café ledger history, or — signed out — any lease's, for
// staff lookups).
//
// It is a vertical product app, distinct from Loupe (the operator tool) and a
// sibling of loftspace-app/clinic-app/wellness-app. WRITES go browser-direct
// to the Gateway's POST /v1/operations, by default with a staff Bearer token
// (mirrors loftspace-app/clinic-app's own "staff" actor-kind — Charge is
// grantsTo:[operator] scope:any). The Resident view's Me bar additionally
// lets a resident sign in as themselves: OpenTab/Settle also grant
// `consumer` scope=self (packages/cafe-domain/permissions.go), so a
// resident can open or settle THEIR OWN tab with a token minted for their
// own identity (mirrors wellness-app's Me bar). READS are all plain NATS-KV
// lens projections (P5) — no protected Postgres read model exists for café,
// so this app carries no pgxpool.
//
// SAFETY: this app has NO authentication and acts as admin. It binds
// 127.0.0.1 only by default; a non-loopback CAFE_APP_ADDR is an explicit
// opt-in and logs a loud warning at startup.
//
// Environment:
//
//	CAFE_APP_ADDR      HTTP listen address (default: 127.0.0.1:7801)
//	NATS_URL           NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	CAFE_APP_DEV_AUTH  "1" enables the demo staff + resident dev-token minter (loopback bind only).
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
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

	// The primordial admin actor. A missing/invalid bootstrap file is NOT fatal:
	// the UI still serves and /api/* handlers report the unconfigured actor as a
	// clean JSON error.
	var adminActor string
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		logger.Warn("bootstrap file not loaded; opening/charging/settling tabs will report an error until it is present",
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

	signer, err := setupDevSigner(logger, isLoopbackHost(hostOf(addr)))
	if err != nil {
		return err
	}
	if signer == nil {
		logger.Warn("no staff dev-token minter configured (set CAFE_APP_DEV_AUTH); POS/front-desk writes will fail to obtain a Bearer token")
	}

	srv := &server{
		conn:        conn,
		adminActor:  adminActor,
		logger:      logger,
		natsTimeout: natsRequestLimit,
		devSigner:   signer,
		gatewayURL:  envOrDefault("CAFE_APP_GATEWAY_URL", defaultGatewayURL),
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
// loopback host: this app is auth-less and acts as admin, so a non-local bind
// exposes admin control to the network.
func warnIfNonLoopback(logger *slog.Logger, addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		logger.Warn("could not parse CAFE_APP_ADDR host; ensure it binds a loopback address", "addr", addr, "error", err)
		return
	}
	if isLoopbackHost(host) {
		return
	}
	logger.Warn("cafe-app has no auth and acts as admin; binding to a non-local address exposes admin control to the network",
		"addr", addr)
}

// isLoopbackHost reports whether host is a loopback bind. An empty host (the
// bare ":7801" form) means all interfaces and is NOT loopback.
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
