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
//	LATTICE_AUTH_MODE    control-plane capability auth mode: "capability" (default) or "stub"
//	LATTICE_CONTROL_JWT_KEYS_DIR       directory of <kid>.pem trusted actor-JWT public keys —
//	                                   unset (and dev mode off) keeps Fire 1's self-asserted
//	                                   HeaderActor (control-plane-capability-authz-design.md)
//	LATTICE_CONTROL_JWT_DEV_MODE       "true" to additionally trust the checked-in Gateway dev
//	                                   key (dev/CI only; mint a token with `gateway dev-token`)
//	LATTICE_CONTROL_JWT_DEV_KEY_PATH   override the dev public-key path
//	LATTICE_CONTROL_JWT_ISSUER         optional; required `iss` claim value
//	LATTICE_CONTROL_JWT_AUDIENCE       optional; required `aud` claim member
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
	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/loom/control"
	"github.com/asolgan/lattice/internal/pkgmgr"
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
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	checker, err := wireControlChecker(context.Background(), conn, "loom", controlauth.LoomOps, logger)
	if err != nil {
		return fmt.Errorf("wire control-plane capability checker: %w", err)
	}
	actorVerifier, err := controlauth.WireActorVerifierFromEnv(context.Background(), conn, logger)
	if err != nil {
		return fmt.Errorf("wire control-plane actor verifier: %w", err)
	}

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

	controlSvc := control.NewService(engine, checker, logger)
	controlSvc.SetActorVerifier(actorVerifier)
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

// wireControlChecker builds the control-plane capability checker
// (control-plane-capability-authz-design.md Fire 1b). Default LATTICE_AUTH_MODE
// is `capability` — mirrors cmd/processor's step-3 default; `stub` remains
// available for dev/test behind the same explicit env knob (one knob, no
// second CTRL-specific one, design §3.3). rbacRolesActive + systemActorKeys
// mirror the Processor's step-3 platform routing so the checker reads the
// same key the Processor would for a given actor. Preflight logs+alerts
// (never blocks startup) if the configured operator actor's grant is
// unresolvable.
func wireControlChecker(ctx context.Context, conn *substrate.Conn, component string, ops map[string]controlauth.OpMeta, logger *slog.Logger) (*controlauth.CapabilityKVChecker, error) {
	mode := controlauth.AuthMode(envOrDefault("LATTICE_AUTH_MODE", string(controlauth.AuthModeCapability)))

	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	rbacInstalled, err := pkgmgr.IsPackageInstalled(probeCtx, conn, "rbac-domain")
	if err != nil {
		probeCancel()
		return nil, fmt.Errorf("probe rbac-domain install state: %w", err)
	}
	systemActorKeys, err := bootstrap.SystemActorKeys(probeCtx, conn)
	probeCancel()
	if err != nil {
		return nil, fmt.Errorf("discover system actor keys: %w", err)
	}

	alerts := controlauth.NewHealthAlertEmitter(conn, bootstrap.HealthKVBucket, logger)
	checker := controlauth.NewCapabilityKVChecker(component, ops, conn, bootstrap.CapabilityKVBucket,
		systemActorKeys, rbacInstalled, mode, alerts, logger)

	operatorActor := os.Getenv("LATTICE_CONTROL_OPERATOR_ACTOR_KEY")
	preflightCtx, preflightCancel := context.WithTimeout(ctx, 10*time.Second)
	controlauth.Preflight(preflightCtx, checker, operatorActor, logger)
	preflightCancel()

	logger.Info("control-plane checker wired",
		"component", component, "authMode", string(mode), "rbacRolesActive", rbacInstalled,
		"systemActors", len(systemActorKeys))
	return checker, nil
}
