// cmd/chronicler — Lattice Chronicler binary (orchestration-history-read-
// model-design.md Fork C, re-ratified 2026-07-06): the event→row
// materializer host, standalone from Refractor. Discovers eventStream-kind
// lens definitions from `vtx.meta.>` and runs one durable core-events
// consumer per definition, upserting projected rows into their declared
// NATS-KV bucket. Submits no ops and never touches Core KV directly (P2:
// state changes via ops; this component writes only its own lens targets +
// Health KV).
//
// Environment:
//
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	CHRONICLER_INSTANCE   instance id (default: auto-generated chronicler-<NanoID>)
//
// Logs to stderr in slog text format. Exits non-zero on startup failure;
// graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/chronicler"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("chronicler exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)

	instance := os.Getenv("CHRONICLER_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "chronicler-" + id
	}

	logger.Info("chronicler starting", "natsURL", natsURL, "instance", instance)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-chronicler:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	host := chronicler.NewHost(chronicler.HostConfig{
		Conn:         conn,
		CoreKVBucket: bootstrap.CoreKVBucket,
		EventsStream: bootstrap.CoreEventsStreamName,
		Instance:     instance,
		Logger:       logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("signal received; shutting down", "signal", sig.String())
		cancel()
	}()

	if err := host.Start(ctx); err != nil {
		return fmt.Errorf("host: %w", err)
	}

	reporter := healthkv.New(healthkv.Config{
		Conn:      conn,
		Bucket:    bootstrap.HealthKVBucket,
		Component: "chronicler",
		Instance:  instance,
		Logger:    logger,
		Probe: func(context.Context) healthkv.Snapshot {
			count, ids := host.Active()
			return healthkv.Snapshot{
				Status:  healthkv.StatusHealthy,
				Metrics: map[string]any{"activeDefinitions": count, "definitionIds": ids},
			}
		},
	})

	logger.Info("chronicler ready", "instance", instance)
	reporter.Run(ctx)
	logger.Info("chronicler exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
