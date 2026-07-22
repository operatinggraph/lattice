// cmd/processor — Lattice Processor binary.
//
// Connects to NATS, registers one durable JetStream consumer per operation lane
// (default / urgent / system / meta) on a substrate ConsumerSupervisor, and
// drives the full 9-step commit path on each delivered operation envelope. The
// meta lane is serialized (MaxAckPending=1, Contract #2 §3.7); the legacy
// single processor-main durable is retired on startup (one-time migration).
//
// Environment:
//
//	NATS_URL                          NATS server URL (default: nats://localhost:4222)
//	NATS_NKEY                         path to a per-component NKey seed file (transport-authorization credential; empty = anonymous)
//	NATS_CREDS                        path to a NATS JWT creds file (alternative to NATS_NKEY; at most one is set)
//	BOOTSTRAP_JSON_PATH               path to lattice.bootstrap.json (default: ./lattice.bootstrap.json) — supplies
//	                                  bootstrap.RoleOperatorID, which the system-actor discovery probe below
//	                                  (bootstrap.SystemActorKeys) matches holdsRole links against.
//	LATTICE_AUTH_MODE                 capability (default) | stub (test/dev — emits stub-auth-active alert)
//	LATTICE_AUTH_TRACE_ALLOW_DECISIONS  "true" to also trace ALLOWED decisions (default: "false" — denial-only per FR23)
//	PROCESSOR_INSTANCE                instance id (default: auto-generated proc-<NanoID>)
//	PROCESSOR_STREAM                  JetStream stream name (default: core-operations)
//	HEALTH_INTERVAL_SEC               heartbeat interval in seconds (default: 10, minimum: 10 per NFR-O1)
//	LATTICE_PROCESSOR_LANES_<LANE>_CONSUMERS  per-lane pump concurrency (LANE = DEFAULT|URGENT|SYSTEM|META;
//	                                  defaults default=2/urgent=4/system=2/meta=1; meta is always clamped to 1)
//	LATTICE_VAULT_MASTER_KEK          base64 32-byte master KEK for the local envelope-encryption
//	                                  Vault backend (Contract #3 §3.10). Exactly one of this or
//	                                  LATTICE_VAULT_MASTER_KEK_FILE must be set — the process refuses
//	                                  to start otherwise.
//	LATTICE_VAULT_MASTER_KEK_FILE     path to a file holding the base64 master KEK (deploy/nkeys/*.nk
//	                                  seed-file posture). Alternative to LATTICE_VAULT_MASTER_KEK.
//	LATTICE_VAULT_KEK_VERSION         label for the configured KEK, for future rotation detection
//	                                  (default: "v1")
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
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/privacyworker"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/outbox"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("processor exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		return fmt.Errorf("load bootstrap JSON: %w", err)
	}

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	// Capability is the only auth mode a running Processor may use. `stub`
	// (allow-all) is retired as a deployable posture — a deployed binary refuses
	// to start in it so a stray `LATTICE_AUTH_MODE=stub` can never silently
	// disable authorization in a real deployment. The StubAuthorizer type
	// survives only as internal test scaffolding (tests build pipelines directly,
	// never through this entry point).
	authMode := processor.AuthMode(envOrDefault("LATTICE_AUTH_MODE", string(processor.AuthModeCapability)))
	if authMode == processor.AuthModeStub {
		return fmt.Errorf("LATTICE_AUTH_MODE=stub is not permitted for a running Processor — stub (allow-all) auth is retired as a deployable posture; use capability")
	}
	traceAllowDecisions := os.Getenv("LATTICE_AUTH_TRACE_ALLOW_DECISIONS") == "true"

	instance := os.Getenv("PROCESSOR_INSTANCE")
	if instance == "" {
		id, err := substrate.NewNanoID()
		if err != nil {
			return fmt.Errorf("generate instance id: %w", err)
		}
		instance = "proc-" + id
	}

	stream := envOrDefault("PROCESSOR_STREAM", "core-operations")
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
		"stream", stream,
		"lanes", "default,urgent,system,meta",
	)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-processor:" + instance,
		MaxReconnects: -1,
		ReconnectWait: 1 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate connect: %w", err)
	}
	defer conn.Close()

	// Class-aware platform routing is unconditional: the kernel-seeded system
	// actors read a UNION of their cap.<actor> anchor and cap.roles.<actor>,
	// every other actor reads cap.roles.<actor> alone. This is correct whether
	// or not rbac-domain is installed — an absent cap.roles.<actor> is an empty
	// skip in the union read (capabilitykv.ReadAndMerge), so a fresh kernel
	// degrades to the anchor floor for system actors and deny-by-absence for
	// ordinary actors, exactly the rbac-absent posture. It is deliberately NOT
	// gated on an rbac-install probe: that probe ran once at startup, so a
	// Processor booted before packages install (the kernel-first `make up`
	// order) latched the pre-install state for its whole life and denied every
	// package-granted actor even after rbac-domain landed — the bug that
	// blocked running capability mode by default. SystemActorKeys are
	// primordial (fixed at bootstrap), so discovering them once here is stable
	// for the process lifetime.
	discCtx, discCancel := context.WithTimeout(context.Background(), 10*time.Second)
	systemActorKeys, err := bootstrap.SystemActorKeys(discCtx, conn)
	discCancel()
	if err != nil {
		return fmt.Errorf("discover system actor keys: %w", err)
	}
	authWiring := processor.AuthWiring{
		RbacRolesActive: true,
		SystemActorKeys: systemActorKeys,
	}
	logger.Info("step-3 platform routing wired (class-aware, unconditional)",
		"systemActors", len(systemActorKeys))

	v, err := loadVault(logger)
	if err != nil {
		return err
	}

	cp, hb, err := processor.MakePipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket, authMode, traceAllowDecisions, logger, instance, authWiring, v)
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

	// One-time durable migration: retire the legacy single all-lanes
	// processor-main consumer (the Fire-1 model) before registering the per-lane
	// durables. The new lane durables start DeliverAll, so the retained stream
	// re-delivers to them; already-committed ops short-circuit at the step-2 dedup
	// tracker (durable — the op-vertex tracker is not pruned), so the migration is
	// idempotent with no double-commit and no data loss. Assumes the MVP
	// single-instance / sequential-restart deploy model. Best-effort: a failed
	// cleanup (e.g. a transient NATS blip, or a peer instance mid-deploy still
	// using the durable) only leaves an orphaned, pumpless processor-main parked on
	// the stream — harmless — so it must NOT block the Processor from serving; log
	// and continue rather than abort startup.
	if err := conn.DeleteStreamConsumer(ctx, stream, processor.LegacyDurable); err != nil {
		logger.Warn("legacy durable retirement failed; continuing (orphaned consumer is harmless)",
			"durable", processor.LegacyDurable, "error", err)
	}

	// Register one operation consumer per lane on a substrate ConsumerSupervisor
	// (the same supervised pump Loom/Weaver/Refractor use). Each Add creates the
	// lane's durable and starts its pump goroutine, so the supervisor must be
	// ready before the heartbeater so the heartbeat can read each lane's real
	// backlog (lane_lag) via the supervisor. The meta lane is pinned to
	// MaxAckPending=1 (Contract #2 §3.7) inside LaneSpecs; lanes drain on
	// independent pumps (priority isolation — urgent no longer queues behind
	// default).
	laneConsumers := processor.LaneConsumers(os.Getenv)
	logger.Info("per-lane pump concurrency resolved",
		"default", laneConsumers["default"], "urgent", laneConsumers["urgent"],
		"system", laneConsumers["system"], "meta", laneConsumers["meta"])
	sup := substrate.NewConsumerSupervisor(conn)
	for _, spec := range processor.LaneSpecs(stream, cp.SupervisedHandler(), 30*time.Second, laneConsumers, logger) {
		if err := sup.Add(ctx, spec); err != nil {
			cancel()
			return fmt.Errorf("register %q lane consumer: %w", spec.Name, err)
		}
	}
	defer sup.Stop()
	hb.AttachBacklogReader(sup, processor.LaneDurables())

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

	// Start the privacy-worker: the async half of crypto-shredding (design
	// §2.4, Fire 3) — a durable consumer on events.privacy.keyShredded that
	// calls Vault.ShredKey. Shares `v`, the SAME Vault instance the commit
	// path decrypts/encrypts through (internal/privacyworker's package doc:
	// this is load-bearing, not just convenient — a separately-constructed
	// instance would not observe the shred). The privacy service actor
	// (Fire 4b finalization recording) is graph-discovered like the system
	// actors above — absent on a pre-v15 kernel, which disables recording
	// without disabling the shred.
	privacyCtx, privacyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	privacyActorKey, err := bootstrap.PrivacyActorKey(privacyCtx, conn)
	privacyCancel()
	if err != nil {
		return fmt.Errorf("discover privacy service actor: %w", err)
	}
	privacyWorker := privacyworker.New(privacyworker.Config{
		Conn:         conn,
		EventsStream: bootstrap.CoreEventsStreamName,
		Vault:        v,
		Logger:       logger,
		ActorKey:     privacyActorKey,
	})
	privacyWorkerDone := make(chan struct{})
	go func() {
		defer close(privacyWorkerDone)
		if perr := privacyWorker.Run(ctx); perr != nil && !errors.Is(perr, context.Canceled) {
			logger.Error("privacy-worker exited with error", "error", perr)
		}
	}()

	// Host the Vault decrypt RPC (lattice.vault.decrypt) on the Processor's
	// authoritative Vault instance — the trusted-tool plaintext read path
	// (vault-crypto-shredding-design.md §2.3): Loupe already holds an
	// identity's piiKey Envelope + a sensitive aspect's Ciphertext from its
	// Core-KV inspector reads, and calls this responder for plaintext rather
	// than holding the master KEK itself. It MUST run here, not in Refractor's
	// separate KEK-only Vault: only this instance carries the live shredded-set,
	// and reads of the durable piiKey.shredded flag return ErrKeyShredded either
	// way. Caller authorization is at the NATS transport (only Loupe + the
	// Processor may publish lattice.vault.decrypt — deploy/gen-dev-nkeys,
	// proven by internal/natsperm); the responder self-stops on ctx cancel.
	vaultSvc := vault.NewService(v, logger)
	if err := vaultSvc.StartNATSListener(ctx, conn.NATS()); err != nil {
		cancel()
		return fmt.Errorf("start vault decrypt responder: %w", err)
	}

	// Host the op-status RPC (lattice.op.status) — the sanctioned way for any
	// op-submitting component to ask "did my operation land?" without a
	// Core-KV read grant (op-status-read-surface-design.md Fire 1). It
	// projects ONLY the Contract #4 tracker (vtx.op.<requestId>); caller
	// authorization is at the NATS transport (bridge + future migrating
	// callers hold the pub-allow, proven by internal/natsperm).
	opStatusSvc := opstatus.NewService(conn, bootstrap.CoreKVBucket, logger)
	if err := opStatusSvc.StartNATSListener(ctx, conn.NATS()); err != nil {
		cancel()
		return fmt.Errorf("start op-status responder: %w", err)
	}

	// Emit the Vault's own Health-KV heartbeat group (health.vault.<instance>,
	// Contract #5 §5.4 Vault baseline) so it renders as a distinct map node with
	// its own custody/shred metrics, rather than riding the Refractor
	// heartbeat. Co-hosted with the Processor, so the instance id is shared; the
	// probe reports live counters off the same Vault instance the commit path,
	// the decrypt responder, and the privacy-worker all use.
	vaultHealth := healthkv.New(healthkv.Config{
		Conn:      conn,
		Bucket:    bootstrap.HealthKVBucket,
		Component: "vault",
		Instance:  instance,
		Interval:  time.Duration(hbSec) * time.Second,
		Logger:    logger,
		Probe: func(context.Context) healthkv.Snapshot {
			s := v.Stats()
			return healthkv.Snapshot{
				Status: healthkv.StatusHealthy,
				Metrics: map[string]any{
					"backend":                   s.Backend,
					"vault_calls_total":         s.DecryptCalls,
					"encrypt_calls_total":       s.EncryptCalls,
					"keyshredded_handled_total": s.ShredCalls,
					"dek_cache_size":            s.DEKCacheSize,
					"keys_shredded":             s.ShreddedCount,
				},
			}
		},
	})
	vaultHealthDone := make(chan struct{})
	go func() {
		defer close(vaultHealthDone)
		vaultHealth.Run(ctx)
	}()

	logger.Info("processor ready",
		"instance", instance,
		"healthKey", "health.processor."+instance,
		"vaultDecryptSubject", vault.DecryptSubject,
		"vaultHealthKey", "health.vault."+instance,
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
	<-privacyWorkerDone
	<-vaultHealthDone
	logger.Info("processor exited cleanly", "instance", instance)
	return nil
}

// loadVault wires the local envelope-encryption Vault backend (design §2.5
// Path A) backing commit-path step 6.5's sensitive-aspect crypto. The master
// KEK is read from LATTICE_VAULT_MASTER_KEK (inline base64) if set, else
// from the file at LATTICE_VAULT_MASTER_KEK_FILE (base64, trailing
// whitespace trimmed) — the same seed-file posture as deploy/nkeys/*.nk.
// Neither set is a startup failure: sensitive-aspect writes would otherwise
// silently land as plaintext, which is worse than refusing to start.
func loadVault(logger *slog.Logger) (*vault.LocalBackend, error) {
	envVar, fileVar := "LATTICE_VAULT_MASTER_KEK", "LATTICE_VAULT_MASTER_KEK_FILE"
	var kek []byte
	var err error
	switch {
	case os.Getenv(envVar) != "":
		kek, err = vault.MasterKEKFromEnv(envVar)
	case os.Getenv(fileVar) != "":
		kek, err = vault.MasterKEKFromFile(os.Getenv(fileVar))
	default:
		return nil, fmt.Errorf("vault: neither %s nor %s is set; refusing to start without a master KEK (a sensitive-aspect write would otherwise land as plaintext)", envVar, fileVar)
	}
	if err != nil {
		return nil, fmt.Errorf("load vault master KEK: %w", err)
	}
	v, err := vault.NewLocalBackend(kek, envOrDefault("LATTICE_VAULT_KEK_VERSION", ""))
	if err != nil {
		return nil, fmt.Errorf("construct vault backend: %w", err)
	}
	logger.Info("vault wired", "backend", "local")
	return v, nil
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
