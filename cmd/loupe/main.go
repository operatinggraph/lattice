// cmd/loupe — Loupe, an internal trusted view-and-control web app over a
// running Lattice deployment.
//
// Loupe connects to NATS as the primordial admin actor and serves a local web
// UI plus a JSON API: browse Core KV, observe Health, drive the Refractor /
// Weaver / Loom control planes, list packages, and submit operations. The
// browser is a thin view; this Go server does all NATS I/O.
//
// SAFETY: Loupe has NO authentication and acts as admin. It binds 127.0.0.1
// only by default. A non-loopback LOUPE_ADDR is an explicit opt-in and logs a
// loud warning at startup — a trusted-deployment tool must not silently become
// an auth-less network-wide admin handle.
//
// Environment:
//
//	LOUPE_ADDR           HTTP listen address (default: 127.0.0.1:7777)
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//
// Logs to stderr in slog text format. The server starts even when NATS is
// unreachable or the bootstrap file is missing: the UI is served and each
// /api/* call returns a JSON error the UI renders, never a crash.
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

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr      = "127.0.0.1:7777"
	natsRequestLimit = 8 * time.Second
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
	})
	if err != nil {
		logger.Warn("NATS connect failed at startup; serving UI, /api/* will report errors until NATS is reachable",
			"natsURL", natsURL, "error", err)
	} else {
		logger.Info("connected to NATS", "natsURL", natsURL)
		defer conn.Close()
	}

	srv := &server{
		conn:        conn,
		adminActor:  adminActor,
		logger:      logger,
		natsTimeout: natsRequestLimit,
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
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
// other than loopback. Loupe is auth-less and acts as admin; a non-local bind
// exposes admin control to the network. A bare ":<port>" (all interfaces) and
// any explicit non-loopback host trip the warning.
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
	logger.Warn("Loupe has no auth and acts as admin; binding to a non-local address exposes admin control to the network",
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
