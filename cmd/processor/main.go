// cmd/processor — Lattice Processor binary.
//
// Connects to NATS, ensures the durable JetStream consumer exists, and drives
// the full 10-step commit path on each delivered operation envelope.
//
// Environment:
//
//	NATS_URL                          NATS server URL (default: nats://localhost:4222)
//	LATTICE_AUTH_MODE                 capability (default) | stub (test/dev — emits stub-auth-active alert)
//	LATTICE_AUTH_TRACE_ALLOW_DECISIONS  "true" to also trace ALLOWED decisions (default: "false" — denial-only per FR23)
//	PROCESSOR_INSTANCE                instance id (default: auto-generated proc-<NanoID>)
//	PROCESSOR_DURABLE                 JetStream durable consumer name (default: processor-main)
//	PROCESSOR_STREAM                  JetStream stream name (default: core-operations)
//	PROCESSOR_FILTER                  comma-separated subject filters (default: ops.default,ops.urgent,ops.system,ops.meta)
//	HEALTH_INTERVAL_SEC               heartbeat interval in seconds (default: 10, minimum: 10 per NFR-O1)
//
// Logs to stderr in slog text format. Exits non-zero on any startup
// failure; on graceful shutdown (SIGINT/SIGTERM) the heartbeater emits a
// `shuttingDown` Health KV entry and the binary exits 0.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("processor exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	// Default LATTICE_AUTH_MODE is `capability`. The stub mode remains available
	// behind an explicit env knob for dev/test deployments; operators selecting
	// it see WARN logs + a Health KV `stub-auth-active` alert.
	authMode := processor.AuthMode(envOrDefault("LATTICE_AUTH_MODE", string(processor.AuthModeCapability)))
	traceAllowDecisions := os.Getenv("LATTICE_AUTH_TRACE_ALLOW_DECISIONS") == "true"

	instance := os.Getenv("PROCESSOR_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "proc-" + id
	}

	durable := envOrDefault("PROCESSOR_DURABLE", "processor-main")
	stream := envOrDefault("PROCESSOR_STREAM", "core-operations")
	filterCSV := envOrDefault("PROCESSOR_FILTER", "ops.default,ops.urgent,ops.system,ops.meta")
	filter := splitCSV(filterCSV)
	hbSec := envIntOrDefault("HEALTH_INTERVAL_SEC", 10)
	if hbSec < 10 {
		logger.Warn("HEALTH_INTERVAL_SEC below NFR-O1 minimum (10s); clamping",
			"requested", hbSec, "effective", 10)
		hbSec = 10
	}

	logger.Info("processor starting",
		"natsURL", natsURL,
		"authMode", string(authMode),
		"instance", instance,
		"durable", durable,
		"stream", stream,
		"filter", filter,
	)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-processor:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	cp, hb, err := processor.MakePipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket, authMode, traceAllowDecisions, logger, instance)
	if err != nil {
		return err
	}

	// Override heartbeat interval on the correctly-wired heartbeater from
	// MakePipeline. SetInterval enforces the NFR-O1 10s minimum.
	if hbSec > 10 {
		hb.SetInterval(time.Duration(hbSec) * time.Second)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start heartbeater.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		hb.Run(ctx)
	}()

	// Ensure consumer.
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     stream,
		Durable:        durable,
		FilterSubjects: filter,
	}, logger)
	if err != nil {
		cancel()
		<-hbDone
		return err
	}

	// Wire signal handling so SIGINT/SIGTERM cancel ctx and trigger
	// graceful heartbeater shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	consumeErrCh := make(chan error, 1)
	go func() {
		consumeErrCh <- cp.Run(ctx, cons)
	}()

	logger.Info("processor ready",
		"instance", instance,
		"healthKey", "health.processor."+instance,
	)

	select {
	case sig := <-sigCh:
		logger.Info("signal received; shutting down", "signal", sig.String())
		cancel()
	case err := <-consumeErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
			<-hbDone
			return err
		}
	}

	<-hbDone
	logger.Info("processor exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
