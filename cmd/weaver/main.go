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
//	WEAVER_REDELIVERY_DELAY       lane-1's SHORT redelivery floor — the minimum wait before a
//	                              transiently-declined row is redelivered (Go duration, e.g. "5s";
//	                              default: substrate.DefaultRedeliveryDelay)
//	WEAVER_LONG_REDELIVERY_DELAY  lane-1's LONG redelivery floor — the cadence a config-error
//	                              decline stands on, and therefore how fast a package edit is
//	                              taken up by rows already declined (Go duration, e.g. "5m";
//	                              default: substrate.DefaultLongRedeliveryDelay, floored at the
//	                              short delay)
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

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver"
	"github.com/operatinggraph/lattice/internal/weaver/control"
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

	checker, err := wireControlChecker(context.Background(), conn, "weaver", controlauth.WeaverOps, logger)
	if err != nil {
		return fmt.Errorf("wire control-plane capability checker: %w", err)
	}
	actorVerifier, err := controlauth.WireActorVerifierFromEnv(context.Background(), conn, logger)
	if err != nil {
		return fmt.Errorf("wire control-plane actor verifier: %w", err)
	}

	engine := weaver.NewEngine(conn, weaver.Config{
		CoreKVBucket:        bootstrap.CoreKVBucket,
		WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
		WeaverStateBucket:   bootstrap.WeaverStateBucket,
		HealthKVBucket:      bootstrap.HealthKVBucket,
		ActorKey:            actorKey,
		Instance:            instance,
		Lane:                lane,
		// Both lane-1 redelivery floors are operator-settable. The long one is
		// the cadence every config-error decline stands on, so it decides both
		// how fast a package fix reaches rows already declined and how much a
		// stuck population costs per pass; a wrong value must be correctable
		// without a rebuild. Zero means "unset" — Config.withDefaults resolves
		// each to the substrate default and floors the long one at the short.
		RedeliveryDelay:     envDuration("WEAVER_REDELIVERY_DELAY", 0, logger),
		LongRedeliveryDelay: envDuration("WEAVER_LONG_REDELIVERY_DELAY", 0, logger),
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

	controlSvc := control.NewService(engine, checker, logger)
	controlSvc.SetActorVerifier(actorVerifier)
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

// envDuration reads a Go duration from the environment, keeping def when the
// variable is unset or unusable. An unparseable or non-positive value is
// REFUSED rather than applied — a zero or negative floor would resolve back to
// the default anyway, so accepting one would log an override that did not
// happen — and the refusal is warned about rather than fatal: a typo in a
// tuning knob must not stop the engine from starting on its defaults.
// (cmd/object-store-manager's idiom.)
func envDuration(key string, def time.Duration, logger *slog.Logger) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logger.Warn("ignoring invalid duration env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return d
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
	if mode == controlauth.AuthModeStub {
		return nil, fmt.Errorf("LATTICE_AUTH_MODE=stub is not permitted for a running component — stub (allow-all) control auth is retired as a deployable posture; use capability")
	}

	// Class-aware platform routing is unconditional (mirrors cmd/processor's
	// step-3 wiring): system actors read the cap.<actor> ∪ cap.roles.<actor>
	// union, every other actor reads cap.roles.<actor>. Correct whether or not
	// rbac-domain is installed (an absent cap.roles.<actor> is an empty skip in
	// capabilitykv.ReadAndMerge), so it is deliberately NOT gated on a boot-time
	// rbac-install probe — that probe latched the pre-install state for a
	// component booted before packages install and denied every package-granted
	// actor for the process lifetime. SystemActorKeys are primordial (stable
	// post-bootstrap), so a one-time discovery here is enough.
	discCtx, discCancel := context.WithTimeout(ctx, 10*time.Second)
	systemActorKeys, err := bootstrap.SystemActorKeys(discCtx, conn)
	discCancel()
	if err != nil {
		return nil, fmt.Errorf("discover system actor keys: %w", err)
	}

	alerts := controlauth.NewHealthAlertEmitter(conn, bootstrap.HealthKVBucket, logger)
	checker := controlauth.NewCapabilityKVChecker(component, ops, conn, bootstrap.CapabilityKVBucket,
		systemActorKeys, true, mode, alerts, logger)

	operatorActor := os.Getenv("LATTICE_CONTROL_OPERATOR_ACTOR_KEY")
	preflightCtx, preflightCancel := context.WithTimeout(ctx, 10*time.Second)
	controlauth.Preflight(preflightCtx, checker, operatorActor, logger)
	preflightCancel()

	logger.Info("control-plane checker wired (class-aware, unconditional)",
		"component", component, "authMode", string(mode),
		"systemActors", len(systemActorKeys))
	return checker, nil
}
