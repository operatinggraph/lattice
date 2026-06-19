// cmd/loom — Lattice Loom engine binary.
//
// Connects to NATS, resolves the primordial identity:loom service-actor key,
// and starts the Loom engine: the durable pattern source (Core KV CDC), the
// per-domain completion consumers, the Transition Engine, and the Actuator.
// Loom shares only internal/substrate with the rest of the platform; all
// cross-component interaction is over NATS.
//
// Environment:
//
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	LOOM_INSTANCE        instance id (default: auto-generated loom-<NanoID>)
//	LOOM_LANE            ops lane for systemOp submission (default: system)
//
// Logs to stderr in slog text format. Exits non-zero on any startup failure;
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

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/loom/control"
	"github.com/asolgan/lattice/internal/substrate"
)

// engineControl is satisfied structurally by *loom.Engine; declared here only as
// a compile-time check that internal/loom/control's interface hasn't drifted from
// the engine's actual method set.
var _ interface {
	ListInstances(ctx context.Context) ([]loom.InstanceSummary, error)
	ListConsumers(ctx context.Context) ([]loom.ConsumerStatus, error)
	InspectInstance(ctx context.Context, instanceID string) (loom.InstanceDetail, error)
	PauseConsumer(ctx context.Context, name string) (string, error)
	ResumeConsumer(ctx context.Context, name string) error
} = (*loom.Engine)(nil)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("loom exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	lane := envOrDefault("LOOM_LANE", "system")

	instance := os.Getenv("LOOM_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "loom-" + id
	}

	// Resolve the primordial identity:loom service-actor key (Story 7.3).
	// Uses the strict loader: an absent/invalid bootstrap file is a fatal
	// startup error, never a freshly-minted (and unrecognized) identity.
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		return fmt.Errorf("load primordial IDs from %s: %w", bootstrapJSONPath, err)
	}
	actorKey := bootstrap.LoomIdentityKey

	logger.Info("loom starting", "natsURL", natsURL, "instance", instance, "actor", actorKey, "lane", lane)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-loom:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	engine := loom.NewEngine(conn, loom.Config{
		CoreKVBucket:    bootstrap.CoreKVBucket,
		LoomStateBucket: bootstrap.LoomStateBucket,
		EventsStream:    bootstrap.CoreEventsStreamName,
		HealthKVBucket:  bootstrap.HealthKVBucket,
		ActorKey:        actorKey,
		Instance:        instance,
		Lane:            lane,
		Logger:          logger,
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

	controlSvc := control.NewService(engine, nil, logger)
	if err := controlSvc.StartNATSListener(ctx, conn.NATS()); err != nil {
		return fmt.Errorf("start control NATS listener: %w", err)
	}
	logger.Info("loom control service started")

	logger.Info("loom ready", "instance", instance)
	if err := engine.Start(ctx); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	logger.Info("loom exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
