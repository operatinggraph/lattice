// cmd/bootstrap — Primordial bootstrap binary for Story 1.3.
//
// Invoked by `make up` after NATS and Postgres containers are healthy.
// Provisions KV buckets + streams, writes all primordial Core KV entries,
// starts the refractor-stub (via subprocess or waits for it to write the
// readiness signal), then exits 0.
//
// Idempotent: if lattice.bootstrap.json already exists, bucket/stream
// provisioning still runs (to recover from partial failures) but primordial
// key seeding is skipped per Contract #7 §7.4.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
)

const defaultBootstrapJSONPath = "./lattice.bootstrap.json"
const defaultReadyTimeoutSec = 30

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", defaultBootstrapJSONPath)
	timeoutSec := envIntOrDefault("BOOTSTRAP_READY_TIMEOUT_SEC", defaultReadyTimeoutSec)

	logger.Info("lattice bootstrap starting", "natsURL", natsURL, "bootstrapJSON", bootstrapJSONPath)

	// Load existing primordial IDs from lattice.bootstrap.json if it exists
	// (idempotent re-run after a successful prior bootstrap), otherwise
	// generate fresh per-deployment unique NanoIDs in memory. The JSON is
	// persisted ONLY AFTER SeedPrimordial succeeds, so an interrupted
	// bootstrap doesn't leave a stale file pointing to nonexistent keys.
	freshlyGenerated, err := bootstrap.LoadOrGenerate(bootstrapJSONPath)
	if err != nil {
		logger.Error("failed to load or generate primordial IDs", "error", err)
		os.Exit(1)
	}
	if freshlyGenerated {
		logger.Info("generated fresh primordial IDs for this deployment (in-memory)",
			"bootstrapIdentity", bootstrap.BootstrapIdentityKey)
	} else {
		logger.Info("loaded existing primordial IDs from lattice.bootstrap.json",
			"bootstrapIdentity", bootstrap.BootstrapIdentityKey)
	}

	// Connect to NATS with retry (containers may be slow to accept connections
	// even after healthcheck passes).
	nc, err := connectNATSWithRetry(natsURL, 20, 1*time.Second, logger)
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()
	logger.Info("connected to NATS", "url", nc.ConnectedUrl())

	seeder, err := bootstrap.NewSeeder(nc, logger)
	if err != nil {
		logger.Error("failed to create seeder", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Always provision buckets/streams — idempotent and recovers partial failures.
	logger.Info("provisioning KV buckets and streams")
	if err := seeder.ProvisionBuckets(ctx); err != nil {
		logger.Error("bucket provisioning failed", "error", err)
		os.Exit(1)
	}

	if freshlyGenerated {
		logger.Info("seeding primordial Core KV entries")
		if err := seeder.SeedPrimordial(ctx); err != nil {
			logger.Error("primordial seeding failed", "error", err)
			os.Exit(1)
		}
		// Persist JSON only AFTER successful seeding. Order matters:
		// presence of lattice.bootstrap.json must imply Core KV is seeded.
		if err := bootstrap.Persist(bootstrapJSONPath); err != nil {
			logger.Error("failed to persist lattice.bootstrap.json", "error", err)
			os.Exit(1)
		}
		logger.Info("lattice.bootstrap.json persisted", "path", bootstrapJSONPath)
	} else {
		logger.Info("primordial seeding skipped — already done on prior run")
	}

	// Wait for readiness gate: refractor-stub writes health.bootstrap.complete.
	logger.Info("waiting for readiness gate", "timeout", fmt.Sprintf("%ds", timeoutSec))
	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	if err := bootstrap.WaitForBootstrapComplete(readyCtx, nc, logger); err != nil {
		logger.Error("readiness gate failed", "error", err,
			"suggestion", "check refractor-stub logs; try `make down && make up`")
		os.Exit(1)
	}

	logger.Info("Lattice ready — primordial bootstrap complete")
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

// connectNATSWithRetry retries NATS connection until maxAttempts or success.
func connectNATSWithRetry(url string, maxAttempts int, delay time.Duration, logger *slog.Logger) (*nats.Conn, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		nc, err := nats.Connect(url,
			nats.MaxReconnects(5),
			nats.ReconnectWait(1*time.Second),
		)
		if err == nil {
			return nc, nil
		}
		lastErr = err
		logger.Info("NATS connect attempt failed, retrying", "attempt", i, "error", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("NATS connect failed after %d attempts: %w", maxAttempts, lastErr)
}
