// cmd/bridge — Lattice Bridge engine binary.
//
// Connects to NATS, resolves the primordial identity:bridge service-actor key,
// and starts the bridge engine: a durable consumer on events.external.> that
// dispatches each external-call event to a named registered adapter and posts a
// result op back to core-operations. The bridge shares only internal/substrate
// with the rest of the platform; all cross-component interaction is over NATS.
//
// Environment:
//
//	NATS_URL             NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH  path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	BRIDGE_INSTANCE      instance id (default: auto-generated bridge-<NanoID>)
//	BRIDGE_LANE          ops lane for result-op submission (default: system)
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/bridge"
	"github.com/asolgan/lattice/internal/substrate"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("bridge exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	lane := envOrDefault("BRIDGE_LANE", "system")

	instance := os.Getenv("BRIDGE_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "bridge-" + id
	}

	// Resolve the primordial identity:bridge service-actor key from the bootstrap
	// file. The strict loader makes an absent/invalid bootstrap file a fatal
	// startup error, never a freshly-minted (and unrecognized) identity.
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		return fmt.Errorf("load primordial IDs from %s: %w", bootstrapJSONPath, err)
	}
	actorKey := bootstrap.BridgeIdentityKey

	logger.Info("bridge starting", "natsURL", natsURL, "instance", instance, "actor", actorKey, "lane", lane)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-bridge:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	engine := bridge.NewEngine(conn, bridge.Config{
		CoreKVBucket:   bootstrap.CoreKVBucket,
		EventsStream:   bootstrap.CoreEventsStreamName,
		HealthKVBucket: bootstrap.HealthKVBucket,
		ActorKey:       actorKey,
		Instance:       instance,
		Lane:           lane,
		Logger:         logger,
	})

	// Register the reference adapters (the real Stripe / background-check /
	// legal-doc integrations are Phase 3). A package's external.<adapter> events
	// name these by the same strings. MUST run before Start: the registry has no
	// lock-step with the dispatch path.
	//
	// BRIDGE_FAKE_DECLINE is a demo affordance: a comma-separated set of adapter
	// names (or "all") whose fake returns a terminal decline for EVERY subject,
	// so an operator can drive the declined-application experience live (e.g.
	// `BRIDGE_FAKE_DECLINE=backgroundCheck make up-loftspace`). Empty = the normal
	// clearing fakes. It only affects the stripe/backgroundCheck fakes; docGen's
	// failure path is input-driven (an unsigned/keyless render request).
	decline := parseDeclineSet(os.Getenv("BRIDGE_FAKE_DECLINE"))
	stripe := bridge.NewFakeStripe()
	bgCheck := bridge.NewFakeBackgroundCheck()
	if decline["all"] || decline["stripe"] {
		stripe.SetDeclineAll(true)
	}
	if decline["all"] || decline["backgroundCheck"] {
		bgCheck.SetDeclineAll(true)
	}
	if len(decline) > 0 {
		logger.Warn("bridge: fake-adapter DECLINE mode active (demo affordance)", "decline", os.Getenv("BRIDGE_FAKE_DECLINE"))
	}
	// docGen — the reference external legal-document vendor. Unlike the pure
	// in-memory fakes it writes the rendered artifact's bytes to the
	// core-objects store (the bridge account holds the $O.core-objects.>
	// publish), capped per artifact by OBJECTS_MAX_UPLOAD_BYTES (the same knob
	// the vertical apps' upload paths use).
	docGen := bridge.NewFakeDocGen(conn, bootstrap.CoreObjectsBucket, uploadCapFromEnv(logger))
	for name, adapter := range map[string]bridge.Adapter{
		"stripe":          stripe,
		"backgroundCheck": bgCheck,
		"docGen":          docGen,
	} {
		if err := engine.RegisterAdapter(name, adapter); err != nil {
			return fmt.Errorf("register adapter %q: %w", name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("signal received; shutting down", "signal", sig.String())
		cancel()
	}()

	logger.Info("bridge ready", "instance", instance)
	if err := engine.Start(ctx); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	logger.Info("bridge exited cleanly", "instance", instance)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultUploadCap bounds a single docGen artifact write (OBJECTS_MAX_UPLOAD_BYTES).
const defaultUploadCap = 25 << 20 // 25 MiB

// uploadCapFromEnv resolves the per-artifact object-store write cap from
// OBJECTS_MAX_UPLOAD_BYTES, defaulting to defaultUploadCap — the same
// environment convention the vertical apps' upload paths use.
func uploadCapFromEnv(logger *slog.Logger) int64 {
	capBytes := int64(defaultUploadCap)
	if v := os.Getenv("OBJECTS_MAX_UPLOAD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			capBytes = n
		} else {
			logger.Warn("ignoring invalid OBJECTS_MAX_UPLOAD_BYTES; using default",
				"value", v, "default", int64(defaultUploadCap))
		}
	}
	return capBytes
}

// parseDeclineSet parses BRIDGE_FAKE_DECLINE — a comma-separated set of adapter
// names (or "all") — into a lookup set, trimming blanks and lowercasing nothing
// (adapter names are case-sensitive: "backgroundCheck", "stripe", "all").
func parseDeclineSet(v string) map[string]bool {
	set := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			set[p] = true
		}
	}
	return set
}
