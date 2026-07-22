// cmd/bootstrap — Lattice primordial bootstrap binary.
//
// Invoked by `make up` after NATS and Postgres containers are healthy.
// Provisions KV buckets + streams, writes all primordial Core KV entries,
// then exits 0.
//
// Idempotent: if lattice.bootstrap.json already exists with
// status="committed", bucket/stream provisioning still runs (to recover
// from partial failures) and Core KV is then probed for the primordial set.
// Seeding is skipped only when the bucket confirms the set is present, per
// Contract #7 §7.4; if the file and the bucket disagree — a recreated or
// wiped Core KV behind a surviving committed file — the binary warns and
// re-seeds using the file's stable NanoIDs, so the platform never comes up
// "ready" over an empty Core KV.
//
// Crash recovery: if lattice.bootstrap.json exists with
// status="in-progress", the same NanoIDs are reused and SeedPrimordial
// is re-run. SeedPrimordial's own idempotency guard skips keys that
// already exist in Core KV, so partial-seeding crashes are safe to retry.
//
// Readiness phasing: the §7.5 readiness gate blocks until the admin, Loom,
// and Weaver `cap.*` projections exist — but those are produced by Refractor,
// which `make up` starts AFTER seeding. To avoid a deadlock the binary runs in
// two phases: the seed pass is invoked with the explicit -skip-ready-wait flag
// (provision + seed + mark, no wait), Refractor is started, then a second
// idempotent pass (no flag, seeding skipped) runs the readiness gate. The skip
// is an explicit CLI flag, never an ambient env var, so an exported variable in
// an operator/CI shell cannot leak into the wait pass and silently defeat the
// gate.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

const defaultBootstrapJSONPath = "./lattice.bootstrap.json"
const defaultReadyTimeoutSec = 30

func main() {
	skipReadyWait := flag.Bool("skip-ready-wait", false,
		"seed pass only: provision + seed + mark readiness, then exit without waiting on the cap.* readiness gate")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", defaultBootstrapJSONPath)
	timeoutSec := envIntOrDefault("BOOTSTRAP_READY_TIMEOUT_SEC", defaultReadyTimeoutSec)

	logger.Info("lattice bootstrap starting", "natsURL", natsURL, "bootstrapJSON", bootstrapJSONPath)

	// LoadOrGenerate implements a two-phase commit protocol:
	//   - No file: generates fresh NanoIDs and writes lattice.bootstrap.json
	//     with status="in-progress" before calling SeedPrimordial. Returns
	//     freshlyGenerated=true.
	//   - File with status="in-progress": reuses the existing IDs (crash
	//     recovery). Returns freshlyGenerated=true so SeedPrimordial is
	//     re-run; its idempotency guard skips already-committed keys.
	//   - File with status="committed": loads IDs, skips seeding.
	// This ensures the NanoID set is stable across restarts regardless of
	// where a crash occurred.
	freshlyGenerated, err := bootstrap.LoadOrGenerate(bootstrapJSONPath)
	if err != nil {
		logger.Error("failed to load or generate primordial IDs", "error", err)
		os.Exit(1)
	}
	if freshlyGenerated {
		logger.Info("seeding primordial IDs (fresh or crash-recovery)",
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

	// `lattice.bootstrap.json` is file-local: it records what a bootstrap run
	// once did on some Core KV, not what this Core KV currently holds. A
	// recreated or wiped bucket behind a surviving status="committed" file
	// would otherwise come up "ready" with silently-empty reads. Probe the
	// bucket itself — provisioning above has ensured it exists — and seed on
	// the bucket's answer. The NanoIDs loaded from the file are stable, so
	// re-seeding restores exactly the keys existing packages and data
	// already reference.
	if !freshlyGenerated {
		probeCtx, cancelProbe := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		seeded, err := seeder.PrimordialSeeded(probeCtx)
		cancelProbe()
		if err != nil {
			logger.Error("failed to probe Core KV for the primordial set", "error", err)
			os.Exit(1)
		}
		if !seeded {
			logger.Warn("lattice.bootstrap.json says committed but Core KV holds no op tracker — bucket recreated or wiped; re-seeding with the file's stable NanoIDs",
				"path", bootstrapJSONPath, "key", bootstrap.BootstrapOpKey)
			// Re-open the two-phase commit window before seeding. The file
			// says "committed", but that claim does not cover this Core KV;
			// leaving it would let a seed that dies partway read as complete
			// to the next run — the exact state this probe exists to catch,
			// and one the op tracker's write-first position would then mask.
			// Only the status changes; the NanoIDs are untouched.
			if err := bootstrap.PersistInProgress(bootstrapJSONPath); err != nil {
				logger.Error("failed to mark lattice.bootstrap.json in-progress", "error", err)
				os.Exit(1)
			}
			freshlyGenerated = true
		}
	}

	if freshlyGenerated {
		logger.Info("seeding primordial Core KV entries")
		seedCtx, cancelSeed := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		err := seeder.SeedPrimordial(seedCtx)
		cancelSeed()
		if err != nil {
			logger.Error("primordial seeding failed", "error", err)
			os.Exit(1)
		}
		// Rewrite lattice.bootstrap.json with status="committed" now that
		// seeding has succeeded. The in-progress file written by
		// LoadOrGenerate already holds the stable NanoIDs; this rewrite
		// marks the two-phase commit complete.
		if err := bootstrap.PersistCommitted(bootstrapJSONPath); err != nil {
			logger.Error("failed to persist lattice.bootstrap.json", "error", err)
			os.Exit(1)
		}
		logger.Info("lattice.bootstrap.json committed", "path", bootstrapJSONPath)
	} else {
		logger.Info("primordial seeding skipped — already done on prior run")
	}

	// cmd/bootstrap writes this marker itself because it is the only process
	// guaranteed to run after primordial seeding completes. The subsequent
	// WaitForBootstrapComplete becomes a sanity check that catches its own
	// write — preserving the gate's semantics for downstream poll-based
	// readiness consumers.
	if err := bootstrap.MarkBootstrapComplete(ctx, nc, logger); err != nil {
		logger.Error("write readiness marker failed", "error", err)
		os.Exit(1)
	}

	// The readiness gate (Contract #7 §7.5) blocks on the admin + Loom +
	// Weaver `cap.*` projections, which Refractor produces. On the seed pass
	// Refractor is not running yet, so `make up` passes -skip-ready-wait to
	// defer the gate to a second pass that runs after Refractor is up. This is
	// an explicit per-invocation flag: only the seed pass carries it, so the
	// gate can never be skipped by an ambient/exported env var.
	if *skipReadyWait {
		logger.Warn("readiness gate SKIPPED — seed pass only (-skip-ready-wait); cap.* projections NOT verified")
		return
	}

	logger.Info("waiting for readiness gate", "timeout", fmt.Sprintf("%ds", timeoutSec))
	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	if err := bootstrap.WaitForBootstrapComplete(readyCtx, nc, logger); err != nil {
		logger.Error("readiness gate failed", "error", err,
			"suggestion", "try `make down && make up`")
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

// credentialOpts builds the transport-authorization nats.Option(s) from
// NATS_NKEY / NATS_CREDS (at most one set; both empty ⇒ anonymous). Bootstrap
// is the sanctioned provisioning-time writer (deploy/nats-server.conf's
// "provisioner" user) — it seeds the kernel before the Processor exists, so it
// authenticates the same way every other binary does rather than connecting
// anonymously against an auth-enabled server.
func credentialOpts() ([]nats.Option, error) {
	nkeySeed := envOrDefault("NATS_NKEY", "")
	credsFile := envOrDefault("NATS_CREDS", "")
	if nkeySeed != "" && credsFile != "" {
		return nil, fmt.Errorf("both NATS_NKEY and NATS_CREDS set; exactly one credential may be supplied")
	}
	if nkeySeed != "" {
		nkeyOpt, err := nats.NkeyOptionFromSeed(nkeySeed)
		if err != nil {
			return nil, fmt.Errorf("load NKey seed %q: %w", nkeySeed, err)
		}
		return []nats.Option{nkeyOpt}, nil
	}
	if credsFile != "" {
		return []nats.Option{nats.UserCredentials(credsFile)}, nil
	}
	return nil, nil
}

// connectNATSWithRetry retries NATS connection until maxAttempts or success.
func connectNATSWithRetry(url string, maxAttempts int, delay time.Duration, logger *slog.Logger) (*nats.Conn, error) {
	credOpts, err := credentialOpts()
	if err != nil {
		return nil, fmt.Errorf("credential options: %w", err)
	}
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		opts := append([]nats.Option{
			nats.MaxReconnects(5),
			nats.ReconnectWait(1 * time.Second),
		}, credOpts...)
		nc, err := nats.Connect(url, opts...)
		if err == nil {
			return nc, nil
		}
		lastErr = err
		logger.Info("NATS connect attempt failed, retrying", "attempt", i, "error", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("NATS connect failed after %d attempts: %w", maxAttempts, lastErr)
}
