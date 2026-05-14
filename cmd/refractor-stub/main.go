// cmd/refractor-stub — Minimal readiness watcher for Story 1.3.
//
// Watches Core KV via a durable consumer. When ALL primordial keys have arrived,
// writes health.bootstrap.complete: true to Health KV.
//
// This is NOT the full Refractor (Story 2.1). It does ONE thing: gate readiness.
// The real Refractor (CDC + Lens projection + Capability KV writes) arrives in
// Story 2.1. For Story 1.3, the gate is defined by the brief (handoff brief
// decision #9): presence of all primordial Core KV keys AND the health signal.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)

	logger.Info("refractor-stub starting", "natsURL", natsURL)

	nc, err := connectNATSWithRetry(natsURL, 30, 1*time.Second, logger)
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		logger.Error("failed to create JetStream context", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Open Core KV for watching.
	coreKV, err := openKVWithRetry(ctx, js, bootstrap.CoreKVBucket, 30, logger)
	if err != nil {
		logger.Error("failed to open Core KV", "error", err)
		os.Exit(1)
	}

	// Open Health KV for writing the readiness signal.
	healthKV, err := openKVWithRetry(ctx, js, bootstrap.HealthKVBucket, 30, logger)
	if err != nil {
		logger.Error("failed to open Health KV", "error", err)
		os.Exit(1)
	}

	// Count-only readiness gate: refractor-stub does NOT need to know the
	// specific primordial NanoIDs (which are randomly generated per
	// deployment by cmd/bootstrap). It waits for `PrimordialVertexKeyCount`
	// unique keys to land in Core KV, then writes the readiness signal.
	//
	// Rationale: at `make up` from a clean state, only cmd/bootstrap writes
	// to Core KV before refractor-stub exits, so the count is an exact and
	// race-free signal. Reading lattice.bootstrap.json would create a
	// startup race (refractor-stub starts before cmd/bootstrap writes the
	// file).
	expected := bootstrap.PrimordialVertexKeyCount
	seen := make(map[string]bool, expected)
	logger.Info("watching for primordial keys", "expectedCount", expected)

	// Watch ALL keys in Core KV (watcher delivers existing + new entries).
	watcher, err := coreKV.WatchAll(ctx)
	if err != nil {
		logger.Error("failed to create Core KV watcher", "error", err)
		os.Exit(1)
	}
	defer watcher.Stop() //nolint:errcheck

	for {
		select {
		case entry, ok := <-watcher.Updates():
			if !ok {
				logger.Error("Core KV watcher channel closed unexpectedly")
				os.Exit(1)
			}
			if entry == nil {
				// nil entry = initial load complete (no more existing keys to deliver).
				logger.Debug("Core KV initial load complete")
				continue
			}
			key := entry.Key()
			if !seen[key] {
				seen[key] = true
				logger.Info("observed Core KV key", "key", key,
					"seen", len(seen), "expected", expected)
			}

			// Count-only gate: ready when `expected` unique keys are present.
			if len(seen) >= expected {
				logger.Info("primordial key count reached — writing readiness signal",
					"count", len(seen))
				if writeErr := writeReadinessSignal(ctx, healthKV, len(seen), logger); writeErr != nil {
					logger.Error("failed to write readiness signal", "error", writeErr)
					os.Exit(1)
				}
				logger.Info("refractor-stub: readiness gate satisfied — exiting")
				return
			}

		case <-ctx.Done():
			logger.Error("context cancelled before all keys seen")
			os.Exit(1)
		}
	}
}

// ReadinessSignal is the Health KV document written when bootstrap is complete.
type ReadinessSignal struct {
	Key         string `json:"key"`
	Component   string `json:"component"`
	Instance    string `json:"instance"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	HeartbeatAt string `json:"heartbeatAt"`
	StartedAt   string `json:"startedAt"`
	Uptime      string `json:"uptime"`
	Metrics     map[string]any `json:"metrics"`
	Issues      []any  `json:"issues"`
}

func writeReadinessSignal(ctx context.Context, healthKV jetstream.KeyValue, observedCount int, logger *slog.Logger) error {
	now := time.Now().UTC()
	doc := ReadinessSignal{
		Key:         bootstrap.HealthBootstrapCompleteKey,
		Component:   "bootstrap",
		Instance:    "refractor-stub-primordial",
		Version:     "1.0",
		Status:      "healthy",
		HeartbeatAt: now.Format(time.RFC3339Nano),
		StartedAt:   now.Format(time.RFC3339Nano),
		Uptime:      "PT0S",
		Metrics: map[string]any{
			"primordial_keys_observed": observedCount,
		},
		Issues: []any{},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal readiness signal: %w", err)
	}
	_, err = healthKV.Put(ctx, bootstrap.HealthBootstrapCompleteKey, data)
	if err != nil {
		return fmt.Errorf("put readiness signal: %w", err)
	}
	logger.Info("readiness signal written", "key", bootstrap.HealthBootstrapCompleteKey)
	return nil
}

func openKVWithRetry(ctx context.Context, js jetstream.JetStream, bucket string, maxAttempts int, logger *slog.Logger) (jetstream.KeyValue, error) {
	for i := 1; i <= maxAttempts; i++ {
		kv, err := js.KeyValue(ctx, bucket)
		if err == nil {
			return kv, nil
		}
		logger.Info("KV bucket not yet ready, retrying", "bucket", bucket, "attempt", i, "error", err)
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("KV bucket %q not available after %d attempts", bucket, maxAttempts)
}

func connectNATSWithRetry(url string, maxAttempts int, delay time.Duration, logger *slog.Logger) (*nats.Conn, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		nc, err := nats.Connect(url, nats.MaxReconnects(5), nats.ReconnectWait(time.Second))
		if err == nil {
			return nc, nil
		}
		lastErr = err
		logger.Info("NATS connect attempt failed, retrying", "attempt", i, "error", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("NATS connect failed after %d attempts: %w", maxAttempts, lastErr)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
