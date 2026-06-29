// cmd/processor — Lattice Processor binary.
//
// Connects to NATS, ensures the durable JetStream consumer exists, and drives
// the full 9-step commit path on each delivered operation envelope.
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
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
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

	// Probe rbac-domain install state to route the platform read by actor
	// class. When rbac-domain is installed, ordinary actors read their
	// role-derived grants from cap.roles.<actor> (rbac-domain's projection)
	// while the kernel-seeded system actors keep reading cap.<actor> (the core
	// primordial anchor). When it is absent, the platform read targets
	// cap.<actor> for all actors and ordinary actors deny by absence.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	rbacInstalled, err := pkgmgr.IsPackageInstalled(probeCtx, conn, "rbac-domain")
	if err != nil {
		probeCancel()
		return fmt.Errorf("probe rbac-domain install state: %w", err)
	}
	systemActorKeys, err := bootstrap.SystemActorKeys(probeCtx, conn)
	probeCancel()
	if err != nil {
		return fmt.Errorf("discover system actor keys: %w", err)
	}
	authWiring := processor.AuthWiring{
		RbacRolesActive: rbacInstalled,
		SystemActorKeys: systemActorKeys,
	}
	logger.Info("step-3 platform routing wired",
		"rbacRolesActive", rbacInstalled, "systemActors", len(systemActorKeys))

	cp, hb, err := processor.MakePipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket, authMode, traceAllowDecisions, logger, instance, authWiring)
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

	// Register the operation consumer on a substrate ConsumerSupervisor (the same
	// supervised pump Loom/Weaver/Refractor use). Add both creates the durable and
	// starts its pump goroutine, so the supervisor must be ready before the
	// heartbeater so the heartbeat can read the consumer's real backlog (lane_lag)
	// via the supervisor. A single all-lanes spec preserves the existing
	// processor-main durable (byte-identical config: the same four lane filters,
	// 30s AckWait, DeliverAll) — no lane split, no durable migration.
	sup := substrate.NewConsumerSupervisor(conn)
	spec := substrate.ConsumerSpec{
		Name:           durable,
		Stream:         stream,
		FilterSubjects: filter,
		DeliverPolicy:  substrate.DeliverAll,
		AckWait:        30 * time.Second,
		Handler:        cp.SupervisedHandler(),
		Logger:         logger,
	}
	if err := sup.Add(ctx, spec); err != nil {
		cancel()
		return fmt.Errorf("register operation consumer: %w", err)
	}
	defer sup.Stop()
	hb.AttachBacklogReader(sup, durable)

	// Start heartbeater.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		hb.Run(ctx)
	}()

	// Wire signal handling so SIGINT/SIGTERM cancel ctx and trigger
	// graceful heartbeater shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start the durable transactional-outbox consumer: it publishes each
	// committed operation's persisted EventList to `core-events`, then
	// tombstones the outbox aspect. Shares the substrate connection and the
	// same ctx for clean shutdown on cancel.
	outboxConsumer := outbox.New(conn, bootstrap.CoreKVBucket, logger)
	outboxDone := make(chan struct{})
	go func() {
		defer close(outboxDone)
		if oerr := outboxConsumer.Run(ctx); oerr != nil && !errors.Is(oerr, context.Canceled) {
			logger.Error("outbox consumer exited with error", "error", oerr)
		}
	}()

	logger.Info("processor ready",
		"instance", instance,
		"healthKey", "health.processor."+instance,
	)

	// The supervised pump reconnects internally on transient consume errors, so
	// there is no consume-error channel to select on: the process runs until a
	// shutdown signal arrives.
	sig := <-sigCh
	logger.Info("signal received; shutting down", "signal", sig.String())

	// Stop the operation pump FIRST (it runs on its own context, independent of
	// ctx), so no operation commits after the outbox publisher is torn down — an
	// op that committed in that gap would otherwise defer its event publication
	// to the next process start. Stop is idempotent with the deferred Stop.
	sup.Stop()
	cancel()
	<-hbDone
	<-outboxDone
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
