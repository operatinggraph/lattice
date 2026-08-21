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
//	NATS_URL                  NATS server URL (default: nats://localhost:4222)
//	BOOTSTRAP_JSON_PATH       path to lattice.bootstrap.json (default: ./lattice.bootstrap.json)
//	BRIDGE_INSTANCE           instance id (default: auto-generated bridge-<NanoID>)
//	BRIDGE_LANE               ops lane for result-op submission (default: system)
//	BRIDGE_CAPABILITY_AUTHOR  "real" registers the model-backed capabilityAuthor
//	                          adapter, which reasons over the capability-author
//	                          catalog through the model-runner fleet. Any other
//	                          value, or unset, registers nothing: an authoring
//	                          request then raises the ordinary adapter-missing
//	                          health issue, exactly as it does today. Turning it
//	                          on requires a deployed model-runner (which holds the
//	                          vendor credential; the bridge never does).
//
// Logs to stderr in slog text format. Exits non-zero on any startup failure;
// graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/bridge"
	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
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

	cfg := bridge.Config{
		EventsStream:   bootstrap.CoreEventsStreamName,
		HealthKVBucket: bootstrap.HealthKVBucket,
		ActorKey:       actorKey,
		Instance:       instance,
		Lane:           lane,
		Logger:         logger,
	}
	// BRIDGE_SKIP_ON_REDELIVERY=false disables the optional skip-on-redelivery
	// tracker probe (engine default: enabled). The probe is defense-in-depth —
	// every adapter dedups on the reused idempotencyKey — so running without it
	// trades one redundant idempotent adapter call per redelivery for zero
	// Core KV reads: the operational lever when the probe's read path is
	// unavailable (op-status-read-surface-design.md, interim mitigation).
	if v := os.Getenv("BRIDGE_SKIP_ON_REDELIVERY"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("parse BRIDGE_SKIP_ON_REDELIVERY %q: %w", v, err)
		}
		cfg.SkipOnRedelivery = &on
	}
	engine := bridge.NewEngine(conn, cfg)

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
	// notification — the clinic-reminders vendor (email/SMS). Fired from the
	// existing RecordAppointmentReminder/RecordFollowUpReminder ops' own outbox,
	// not a Loom pattern (clinic-reminders-notification-adapter-design.md).
	notification := bridge.NewFakeNotification()
	for name, adapter := range map[string]bridge.Adapter{
		"stripe":          stripe,
		"backgroundCheck": bgCheck,
		"docGen":          docGen,
		"notification":    notification,
	} {
		if err := engine.RegisterAdapter(name, adapter); err != nil {
			return fmt.Errorf("register adapter %q: %w", name, err)
		}
	}

	// capabilityAuthor — the model-backed AI-authoring adapter, off unless
	// BRIDGE_CAPABILITY_AUTHOR names it. It is the one adapter whose absence is
	// the correct default: it spends money at a vendor, and a platform with no
	// model-runner deployed has nothing for it to talk to. Registering nothing
	// leaves an authoring request on today's adapter-missing path (ack + a
	// health issue), which is a visible config gap rather than a fake
	// fabricating proposals in production.
	//
	// Both the validator and the catalog bucket name are resolved here rather
	// than inside internal/bridge: this is the composition root, so it is the
	// one place allowed to depend on the installer and on package data. The
	// bucket name comes from the owning package's own exported constant, so a
	// rename there cannot leave the adapter reading a bucket nobody projects.
	if os.Getenv("BRIDGE_CAPABILITY_AUTHOR") == "real" {
		author, err := bridge.NewCapabilityAuthor(
			wire.NewClient(conn.NATS()),
			conn,
			capabilityauthor.CapabilityAuthorContextBucket,
			capabilityArtifactVerdict,
		)
		if err != nil {
			return fmt.Errorf("build capabilityAuthor adapter: %w", err)
		}
		if err := engine.RegisterAdapter("capabilityAuthor", author); err != nil {
			return fmt.Errorf("register adapter %q: %w", "capabilityAuthor", err)
		}
		logger.Info("bridge: model-backed capabilityAuthor adapter registered",
			"catalogBucket", capabilityauthor.CapabilityAuthorContextBucket,
			"runnerSubject", wire.GenerateSubject,
			"resultsBucket", wire.ResultsBucket)
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

// bridgeCypherParser adapts ruleengine/full to pkgmgr.CypherParser — the same
// wiring cmd/loupe's review console and the CLI's capability path build. It
// lives at the composition root, not in internal/pkgmgr, for the import-cycle
// reason pkgmgr.CypherParser's own doc explains (full's test binary
// transitively imports pkgmgr).
type bridgeCypherParser struct{}

func (bridgeCypherParser) Parse(ruleBody string) (pkgmgr.SpecLabels, error) {
	facts, err := full.SpecLabels(ruleBody)
	if err != nil {
		return pkgmgr.SpecLabels{}, err
	}
	return pkgmgr.SpecLabels{
		Referenced: facts.Referenced,
		Exhaustive: facts.Exhaustive,
		Expansion:  facts.Expansion,
	}, nil
}

var _ pkgmgr.CypherParser = bridgeCypherParser{}

// capabilityArtifactVerdict is the capabilityAuthor adapter's deterministic
// validator: the same pkgmgr.ValidateCapabilityArtifact boundary Loupe re-runs
// at approve time, so a proposal recorded valid here is one the approve path
// would also accept. It is built at the composition root and injected, keeping
// the installer out of internal/bridge.
//
// The two optional dependencies are deliberately nil. requesterHeld is read only
// for the "grant" kind (a conferred-authority subset check) and the
// sensitive-aspect resolver only for "opMeta"; this adapter authors weaver
// targets and nothing else, so neither is ever consulted — the same nil pair
// Loupe's own weaverTarget/lens check passes.
//
// A validator ERROR is a malformed artifact, not an unknown verdict: the report
// carries the reason and the state fails closed to invalid, so an
// undecodable draft records visibly rather than being admitted for review.
func capabilityArtifactVerdict(kind string, content []byte) (string, string) {
	report, err := pkgmgr.ValidateCapabilityArtifact(kind, json.RawMessage(content), bridgeCypherParser{}, nil, nil)
	if err != nil {
		return bridge.ValidationStateInvalid, "artifact validation failed: " + err.Error()
	}
	if report.Valid {
		return bridge.ValidationStateValid, ""
	}
	return bridge.ValidationStateInvalid, strings.Join(report.Errors, "; ")
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
