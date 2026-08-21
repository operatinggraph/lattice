// cmd/model-runner — Lattice model-runner binary (natural-language-weaver-
// targets-design.md §3.1): the platform's sole external-model egress and sole
// third-party-credential holder. It serves a NATS micro service in a queue
// group, turns each request into exactly one vendor call, and writes the
// outcome to the `model-results` KV bucket under the caller's ref.
//
// It touches no Core KV, submits no operations, and is deploy-opt-in: nothing
// in the platform changes until an instance is running with a key.
//
// Environment:
//
//	NATS_URL                      NATS server URL (default: nats://localhost:4222)
//	NATS_NKEY                     path to the component's NKey seed
//	NATS_CREDS                    path to a creds file (alternative to NATS_NKEY)
//	MODEL_RUNNER_INSTANCE         instance id (default: auto-generated model-runner-<NanoID>)
//	MODEL_RUNNER_MAX_CONCURRENT   simultaneous vendor calls on this instance (default: 2)
//	MODEL_RUNNER_DAILY_CAP        fleet-wide calls per UTC day (default: 20)
//	ANTHROPIC_API_KEY             vendor credential — REQUIRED
//
// Logs to stderr in slog text format. Exits non-zero on startup failure;
// graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/modelrunner"
	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("model-runner exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// A runner without a credential is a misconfiguration, not a degraded
	// mode: it would accept work, claim every ref, and fail every call. Refuse
	// to start instead, loudly.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return errors.New("ANTHROPIC_API_KEY is not set — the model-runner is the platform's vendor-credential " +
			"holder and cannot serve requests without one; set it or do not deploy this binary")
	}

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)

	instance := os.Getenv("MODEL_RUNNER_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "model-runner-" + id
	}

	maxConcurrent := envInt("MODEL_RUNNER_MAX_CONCURRENT", modelrunner.DefaultMaxConcurrent, logger)
	dailyCap := envInt("MODEL_RUNNER_DAILY_CAP", modelrunner.DefaultDailyCap, logger)

	logger.Info("model-runner starting",
		"natsURL", natsURL, "instance", instance,
		"subject", wire.GenerateSubject, "queueGroup", wire.QueueGroup,
		"maxConcurrent", maxConcurrent, "dailyCap", dailyCap)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-model-runner:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	engine, err := modelrunner.New(modelrunner.Config{
		Conn:          conn,
		Vendor:        modelrunner.NewAnthropicVendor(apiKey),
		MaxConcurrent: maxConcurrent,
		DailyCap:      dailyCap,
		RedactStrings: []string{apiKey},
		Logger:        logger,
	})
	if err != nil {
		return err
	}

	// workCtx bounds in-flight vendor calls. It is cancelled only at the very
	// end (or when a drain overruns), NEVER on the first signal: a SIGTERM must
	// DRAIN — let running calls finish — not abort them into StateFailed and
	// burn the caller's one retry slot on each.
	workCtx, workCancel := context.WithCancel(context.Background())
	defer workCancel()

	if err := engine.Start(workCtx); err != nil {
		return err
	}

	reporter := healthkv.New(healthkv.Config{
		Conn:      conn,
		Bucket:    bootstrap.HealthKVBucket,
		Component: "model-runner",
		Instance:  instance,
		Logger:    logger,
		Probe: func(ctx context.Context) healthkv.Snapshot {
			m := engine.Metrics(ctx)
			snap := healthkv.Snapshot{
				Status: healthkv.StatusHealthy,
				Metrics: map[string]any{
					"acceptedTotal":      m.AcceptedTotal,
					"busyTotal":          m.BusyTotal,
					"invalidTotal":       m.InvalidTotal,
					"rejectedTotal":      m.RejectedTotal,
					"dedupTotal":         m.DedupTotal,
					"completedTotal":     m.CompletedTotal,
					"refusedTotal":       m.RefusedTotal,
					"failedTotal":        m.FailedTotal,
					"vendorInputTokens":  m.VendorInputTokens,
					"vendorOutputTokens": m.VendorOutputTokens,
					"inFlight":           m.InFlight,
					"dailyCount":         m.DailyCount,
					"dailyCap":           dailyCap,
				},
			}
			// At or above the cap the fleet is deliberately refusing work — a
			// real operational state an operator should see, not a silent stream
			// of busy acks. dailyCap==0 (the "stop spending" switch) blocks
			// everything and is reported the same way; a negative cap (disabled)
			// never triggers this axis.
			if dailyCap == 0 {
				snap.Status = healthkv.StatusDegraded
				snap.Issues = []healthkv.Issue{{
					Code:     "ModelRunnerDailyCapReached",
					Severity: "warning",
					Message:  "MODEL_RUNNER_DAILY_CAP=0 — every request is answered busy (spending is switched off)",
				}}
			} else if dailyCap > 0 && m.DailyCount >= int64(dailyCap) {
				snap.Status = healthkv.StatusDegraded
				snap.Issues = []healthkv.Issue{{
					Code:     "ModelRunnerDailyCapReached",
					Severity: "warning",
					Message: fmt.Sprintf("daily call cap reached (%d/%d) — requests are answered busy until the UTC day rolls",
						m.DailyCount, dailyCap),
				}}
			}
			return snap
		},
	})

	// The reporter's lifetime is tied to the signal: it heartbeats until we are
	// asked to stop, then emits its shuttingDown beat.
	serveCtx, serveStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer serveStop()

	reporterDone := make(chan struct{})
	go func() {
		reporter.Run(serveCtx)
		close(reporterDone)
	}()

	logger.Info("model-runner ready", "instance", instance)
	<-serveCtx.Done()
	logger.Info("signal received; draining in-flight model calls before shutdown",
		"drainDeadline", engine.VendorTimeout().String())

	// Graceful drain: stop accepting new requests (svc.Stop unsubscribes the
	// endpoint), then let in-flight calls finish — recordTerminal already runs
	// under WithoutCancel, so completions land. Only if a call overruns the
	// drain deadline do we force-cancel the work context. A second signal is not
	// trapped by NotifyContext, so it terminates the process immediately for an
	// impatient operator.
	stopped := make(chan struct{})
	go func() {
		if err := engine.Stop(); err != nil {
			logger.Warn("model-runner: service stop failed", "error", err)
		}
		close(stopped)
	}()

	drainCtx, drainCancel := context.WithTimeout(context.Background(), engine.VendorTimeout())
	defer drainCancel()
	select {
	case <-stopped:
		logger.Info("model-runner drained cleanly", "instance", instance)
	case <-drainCtx.Done():
		logger.Warn("model-runner: drain deadline exceeded; cancelling in-flight calls", "instance", instance)
		workCancel()
		<-stopped
	}

	<-reporterDone
	logger.Info("model-runner exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt reads a non-negative integer setting. ONLY an unset (or unparseable/
// negative) value falls back to the default — an explicit "0" is returned as 0,
// never collapsed to the default. That distinction is load-bearing for the
// daily cap: "0" is the operator's "stop spending" switch (block all), so it
// must reach the engine as 0 and not be silently rewritten to 20. A garbage or
// negative value falls back loudly, so a typo cannot quietly disable the cap.
func envInt(key string, def int, logger *slog.Logger) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		logger.Warn("ignoring unparseable setting; using default", "key", key, "value", raw, "default", def)
		return def
	}
	return n
}
