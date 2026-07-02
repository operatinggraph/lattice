// cmd/weaver — Lattice Weaver engine binary.
//
// Connects to NATS, resolves the primordial identity:weaver service-actor key,
// and starts the Weaver engine: the durable meta.weaverTarget registry source
// (Core KV CDC), the per-target lane-1 violation consumers, the Evaluator/
// Strategist, and the fire-and-forget Actuator. Weaver shares only
// internal/substrate with the rest of the platform; all cross-component
// interaction is over NATS.
//
// Environment:
//
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	WEAVER_INSTANCE      instance id — a single dot-free token, rejected at
//	                     engine start otherwise (default: auto-generated weaver-<NanoID>)
//	WEAVER_LANE          ops lane for remediation-op submission — a single
//	                     dot-free subject token, rejected at engine start
//	                     otherwise (default: system)
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
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver"
	"github.com/asolgan/lattice/internal/weaver/control"
)

// engineControl is satisfied structurally by *weaver.Engine; declared here
// only as a compile-time check that internal/weaver/control's interface
// hasn't drifted from the engine's actual method set.
var _ interface {
	ListTargets(ctx context.Context) ([]weaver.TargetSummary, error)
	Disable(ctx context.Context, targetID string) error
	Enable(ctx context.Context, targetID string) error
	Revoke(ctx context.Context, targetID string) error
} = (*weaver.Engine)(nil)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("weaver exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	lane := envOrDefault("WEAVER_LANE", "system")

	instance := os.Getenv("WEAVER_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "weaver-" + id
	}

	// Resolve the primordial identity:weaver service-actor key.
	// Uses the strict loader: an absent/invalid bootstrap file is a fatal
	// startup error, never a freshly-minted (and unrecognized) identity.
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		return fmt.Errorf("load primordial IDs from %s: %w", bootstrapJSONPath, err)
	}
	actorKey := bootstrap.WeaverIdentityKey

	logger.Info("weaver starting", "natsURL", natsURL, "instance", instance, "actor", actorKey, "lane", lane)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-weaver:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	engine := weaver.NewEngine(conn, weaver.Config{
		CoreKVBucket:        bootstrap.CoreKVBucket,
		WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
		WeaverStateBucket:   bootstrap.WeaverStateBucket,
		HealthKVBucket:      bootstrap.HealthKVBucket,
		ActorKey:            actorKey,
		Instance:            instance,
		Lane:                lane,
		Logger:              logger,
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
	logger.Info("weaver control service started")

	logger.Info("weaver ready", "instance", instance)
	if err := engine.Start(ctx); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	logger.Info("weaver exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
