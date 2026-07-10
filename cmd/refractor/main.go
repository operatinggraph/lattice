// refractor is the Lattice projection engine. It consumes Core KV CDC and
// sources lens definitions from `vtx.meta.>` (filtered by envelope class
// `meta.lens` per data-contracts.md §1.2 line 70).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/keyshredded"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

const (
	coreKVBucket             = "core-kv"
	healthKVBucket           = "health-kv"
	adjacencyKVBucket        = "refractor-adjacency"
	personalInterestKVBucket = "personal-lens-interest"
	defaultHeartbeatEvery    = 10 * time.Second
)

// reservedActivationBuckets mirrors internal/pkgmgr's reservedBucketNames
// (bucketguard.go) as a fail-closed backstop at Refractor lens activation —
// the platform-private buckets a lens must never target, since the nats-kv
// adapter auto-creates/truncates whatever Bucket a lens declares verbatim.
var reservedActivationBuckets = map[string]struct{}{
	bootstrap.CoreKVBucket:            {},
	bootstrap.HealthKVBucket:          {},
	bootstrap.RefractorAdjacencyKV:    {},
	bootstrap.LoomStateBucket:         {},
	bootstrap.WeaverStateBucket:       {},
	bootstrap.PersonalLensInterestKV:  {},
	bootstrap.GatewayRevocationBucket: {},
}

type pipelineEntry struct {
	cancel        context.CancelFunc
	done          chan struct{}
	pipeline      *pipeline.Pipeline
	reporter      *health.Reporter
	canonicalName string // keyed under lensLatency in heartbeats.
	authPlane     bool   // projects the capability-kv authorization surface (projection.IsAuthPlane).
	// secureColumns is the Secure-Lens column set the RUNNING pipeline's
	// decryptor was built from. Hot-reload guards compare an incoming spec
	// against this — not against the last-seen spec — so a refused update
	// cannot poison the baseline and wedge the lens.
	secureColumns []lens.SecureColumn
}

func main() {
	natsURL := flag.String("nats-url", envOr("NATS_URL", nats.DefaultURL), "NATS server URL")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	instance := "rfx-" + randHex(6)
	logger.Info("refractor starting", "instance", instance, "natsURL", *natsURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Substrate is the integration boundary.
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          *natsURL,
		NKeySeedFile: envOr("NATS_NKEY", ""),
		CredsFile:    envOr("NATS_CREDS", ""),
	})
	if err != nil {
		logger.Error("nats connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	nc := conn.NATS()
	js := conn.JetStream()

	// Start the heartbeater early so health.refractor.<instance> shows
	// up in Health KV within 10s of process start (AC #6 + AC #9).
	hb := health.NewLatticeHeartbeater(conn, healthKVBucket, instance, defaultHeartbeatEvery, logger)
	go hb.Run(ctx)

	// The deferred-retry queue for Transient write failures (the failure-tier
	// "deferred retry queue" route, docs/components/refractor-failure-tiers.md).
	// Shared across every pipeline instance — one Run loop for the process;
	// RetryQueue enforces the single-caller invariant itself.
	retryQueue := failure.NewRetryQueue()
	go retryQueue.Run(ctx)

	// Open Core KV and the (pre-provisioned) refractor-adjacency bucket as
	// substrate handles — the read path threads *substrate.KV, not raw jetstream.
	coreKV, err := conn.OpenKV(ctx, coreKVBucket)
	if err != nil {
		logger.Error("open core KV", "bucket", coreKVBucket, "err", err)
		os.Exit(1)
	}
	adjKV, err := conn.OpenKV(ctx, adjacencyKVBucket)
	if err != nil {
		logger.Error("open refractor adjacency KV", "bucket", adjacencyKVBucket, "err", err)
		os.Exit(1)
	}
	healthKVHandle, err := conn.OpenKV(ctx, healthKVBucket)
	if err != nil {
		logger.Error("open health KV", "bucket", healthKVBucket, "err", err)
		os.Exit(1)
	}
	// The Personal Lens's per-device Interest Set registry (Fire PL.2):
	// backs the control plane's "register"/"deregister" ops and the fan-out
	// pipeline's relevance filter (personal-secure-lens-design.md §3.3).
	personalInterestKV, err := conn.OpenKV(ctx, personalInterestKVBucket)
	if err != nil {
		logger.Error("open personal-lens-interest KV", "bucket", personalInterestKVBucket, "err", err)
		os.Exit(1)
	}
	// D1's read-path Capability KV (Contract #6 §6.14) — the Personal Lens's
	// security gate (personal-secure-lens-design.md §3.4, Fire PL.3).
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	if err != nil {
		logger.Error("open capability KV", "bucket", bootstrap.CapabilityKVBucket, "err", err)
		os.Exit(1)
	}

	bootstrapper := consumer.NewBootstrapper(conn, coreKVBucket, adjKV)
	go func() {
		if err := bootstrapper.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("adjacency bootstrap failed — no lenses will start", "err", err)
			stop() // cancel the root context so main exits and the process can restart
		}
	}()

	poolManager := adapter.NewPoolManager()
	controlSvc := control.NewService()
	controlSvc.SetPersonalInterestKV(personalInterestKV)
	checker, err := wireControlChecker(ctx, conn, "refractor", controlauth.RefractorOps, logger)
	if err != nil {
		logger.Error("wire control-plane capability checker", "err", err)
		os.Exit(1)
	}
	controlSvc.SetCapabilityChecker(checker)
	actorVerifier, err := controlauth.WireActorVerifierFromEnv(ctx, conn, logger)
	if err != nil {
		logger.Error("wire control-plane actor verifier", "err", err)
		os.Exit(1)
	}
	controlSvc.SetActorVerifier(actorVerifier)

	// The KeyShredded nullification listener (vault-crypto-shredding-design.md
	// §2.4, Fire 4a) — the Refractor half of crypto-shredding's async
	// finalization; internal/privacyworker (in cmd/processor) is the other,
	// independent consumer of the same event. Targets is empty until a
	// vertical's Phase-A lens opts in (see internal/refractor/keyshredded's
	// package doc) — an empty list is a harmless no-op consumer that still
	// exercises the event and the counters. The privacy service actor (Fire 4b
	// finalization recording) is graph-discovered — absent on a pre-v15
	// kernel, which disables recording without disabling nullification.
	privacyCtx, privacyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	privacyActorKey, paErr := bootstrap.PrivacyActorKey(privacyCtx, conn)
	privacyCancel()
	if paErr != nil {
		logger.Error("discover privacy service actor", "err", paErr)
		os.Exit(1)
	}
	keyShredded := keyshredded.New(keyshredded.Config{
		Conn:         conn,
		EventsStream: bootstrap.CoreEventsStreamName,
		Control:      controlSvc,
		Logger:       logger,
		ActorKey:     privacyActorKey,
	})
	// The Vault backend for Secure-Lens decrypt-at-projection (Contract #3
	// §3.10; vault-crypto-shredding-design.md §2.3 Phase B). Optional: a
	// deployment with no Secure Lens needs no KEK; a Secure Lens activating
	// with no Vault fails closed at startPipeline. A configured-but-invalid
	// KEK is a hard startup failure — silently proceeding would strand every
	// secure lens with a confusing per-lens activation error.
	vaultBackend, vaultErr := loadVault(logger)
	if vaultErr != nil {
		logger.Error("load vault backend", "err", vaultErr)
		os.Exit(1)
	}
	// vaultCalls counts Vault.Decrypt invocations across every Secure Lens for
	// the Contract #5 §5.4 vault_calls_total heartbeat metric. Reports 0 while
	// no secure lens is active (Refractor then makes no Vault calls).
	var vaultCalls atomic.Uint64

	hb.KeyShreddedHandledTotalProvider = keyShredded.HandledTotal
	hb.VaultCallsTotalProvider = vaultCalls.Load
	go func() {
		if err := keyShredded.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("keyshredded listener exited with error", "err", err)
		}
	}()

	var (
		mu       sync.Mutex
		registry = make(map[string]*pipelineEntry)
		wg       sync.WaitGroup
	)

	// Per-Lens latency stats provider for the heartbeater.
	// Falls back to a no-op when no pipeline has a latency buffer.
	hb.LensLatencyProvider = func() map[string]health.LensLatencySnapshot {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]health.LensLatencySnapshot, len(registry))
		for _, entry := range registry {
			if entry.pipeline == nil || entry.canonicalName == "" {
				continue
			}
			buf := entry.pipeline.LatencyBuffer()
			if buf == nil {
				continue
			}
			snap := buf.Snapshot()
			if snap.Count == 0 {
				continue
			}
			out[entry.canonicalName] = health.LensLatencySnapshot{
				Count: snap.Count,
				Mean:  snap.Mean,
				P95:   snap.P95,
				P99:   snap.P99,
			}
		}
		return out
	}

	// LagProvider for the heartbeater — read pending count per lens from each
	// pipeline's supervised consumer (by durable name, via the supervisor).
	hb.LagProvider = func() map[string]uint64 {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]uint64, len(registry))
		for lensID, entry := range registry {
			if entry.pipeline == nil {
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				continue
			}
			out[lensID] = pending
		}
		return out
	}

	// CapabilityLensProvider for the heartbeater — liveness of the auth-plane
	// (capability-kv) lenses for the §5.5 capability backstop. Read-only: status
	// from the lens reporter, lag from the supervised consumer; no authz path,
	// Core KV, or projection is touched. Errors collapse to a skipped lens.
	hb.CapabilityLensProvider = func() []health.CapabilityLensStatus {
		mu.Lock()
		entries := make([]*pipelineEntry, 0, len(registry))
		for _, entry := range registry {
			if entry.authPlane && entry.pipeline != nil && entry.reporter != nil {
				entries = append(entries, entry)
			}
		}
		mu.Unlock()

		out := make([]health.CapabilityLensStatus, 0, len(entries))
		for _, entry := range entries {
			st, err := entry.reporter.GetStatus(context.Background())
			if err != nil {
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				continue
			}
			pauseReason := ""
			if st.PauseReason != nil {
				pauseReason = *st.PauseReason
			}
			out = append(out, health.CapabilityLensStatus{
				CanonicalName: entry.canonicalName,
				RuleID:        st.RuleID,
				Status:        st.Status,
				PauseReason:   pauseReason,
				ConsumerLag:   pending,
			})
		}
		return out
	}

	// LensProvider for the heartbeater — the generalized (non-auth-plane)
	// projection-liveness backstop (lens-projection-liveness-design.md §3.3).
	// Sibling of CapabilityLensProvider above: same read-only shape, scoped to
	// business lenses so the auth-plane path stays untouched (§5.1). Reads the
	// in-process Progress() live every beat (independent of the LagPoller's 5s
	// cycle), so the backstop alert survives a LagPoller stall (design §5.5).
	hb.LensProvider = func() []health.LensLivenessStatus {
		mu.Lock()
		entries := make([]*pipelineEntry, 0, len(registry))
		for _, entry := range registry {
			if !entry.authPlane && entry.pipeline != nil && entry.reporter != nil {
				entries = append(entries, entry)
			}
		}
		mu.Unlock()

		out := make([]health.LensLivenessStatus, 0, len(entries))
		for _, entry := range entries {
			st, err := entry.reporter.GetStatus(context.Background())
			if err != nil {
				continue
			}
			pending, err := entry.pipeline.Pending(context.Background())
			if err != nil {
				continue
			}
			pauseReason := ""
			if st.PauseReason != nil {
				pauseReason = *st.PauseReason
			}
			out = append(out, health.LensLivenessStatus{
				CanonicalName:   entry.canonicalName,
				RuleID:          st.RuleID,
				Status:          st.Status,
				PauseReason:     pauseReason,
				ProjectionLag:   pending,
				LastProjectedAt: entry.pipeline.Progress().LastProjectedAt,
			})
		}
		return out
	}

	buildAdapter := func(r *lens.Rule) (adapter.Adapter, error) {
		// DeleteMode is defaulted to "hard" and validated upstream (Parse /
		// translateSpec); re-parse here to obtain the typed value for the adapter.
		deleteMode, err := adapter.ParseDeleteMode(r.Into.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: delete_mode: %w", r.ID, err)
		}
		switch r.Into.Target {
		case "nats_kv":
			// Fail-closed mirror of pkgmgr's install-time reserved-bucket guard
			// (internal/pkgmgr/bucketguard.go): a platform-private bucket must
			// never be auto-created/truncated as a lens target, even if a lens
			// spec reached activation by a path that skipped pkgmgr's check
			// (hand-authored spec, direct install).
			if _, reserved := reservedActivationBuckets[r.Into.Bucket]; reserved {
				return nil, fmt.Errorf("lens %q: Bucket %q is a platform-private bucket, never a lens target — refusing to open/create it", r.ID, r.Into.Bucket)
			}
			// Open the target bucket as a substrate handle; create it first if it
			// does not exist (pre-provisioned buckets like capability-kv are reused).
			targetKV, err := conn.OpenKV(ctx, r.Into.Bucket)
			if err != nil {
				if _, cerr := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: r.Into.Bucket}); cerr != nil {
					return nil, cerr
				}
				targetKV, err = conn.OpenKV(ctx, r.Into.Bucket)
				if err != nil {
					return nil, err
				}
			}
			return adapter.New(targetKV, r.Into.Key, deleteMode)
		case "postgres":
			pool, err := poolManager.Acquire(ctx, r.Into.DSN)
			if err != nil {
				return nil, err
			}
			// A grant lens projects to the shared actor_read_grants table through
			// the seq-guarded grant writer (Contract #6 §6.14). The table is
			// provisioned out-of-band; the adapter's Probe verifies its posture and
			// the lens starts infra-paused until the verify passes (verify-and-
			// pause). Refractor issues no runtime DDL.
			if r.Into.GrantTable {
				gw, err := adapter.NewPostgresGrantWriter(pool, r.Into.QueryTimeout)
				if err != nil {
					return nil, err
				}
				return adapter.NewGrantWriterAdapter(gw)
			}
			// A protected read model (read-path authorization, D1.3): the RLS-locked
			// business table (FORCE ROW LEVEL SECURITY + the §6.14 set-membership
			// policy) and the actor_read_grants table its policy references are both
			// provisioned out-of-band. The adapter's Probe verifies the posture and
			// the lens starts infra-paused until it passes — Refractor projects
			// nothing into a table that is not locked down, and issues no DDL. A
			// non-protected table is also provisioned out-of-band.
			base, err := adapter.NewPostgresAdapter(pool, r.Into.Table, r.Into.Key, r.Into.QueryTimeout, deleteMode)
			if err != nil {
				return nil, err
			}
			if !r.Into.Protected {
				return base, nil
			}
			return adapter.NewProtectedAdapter(base, r.Into.ArrayColumns, r.Into.Columns)
		case "nats_subject":
			// The Personal Lens transport (personal-secure-lens-design.md Fire 1):
			// a fire-and-forget per-actor delta publish, not a KV write — no
			// bucket/table to open, just the backing SYNC-style stream, which the
			// adapter ensures (JIT, mirroring the nats_kv bucket-create-if-absent
			// above).
			return adapter.NewNatsSubjectAdapter(ctx, conn, r.Into.SubjectPrefix, r.Into.Stream, r.Into.Key)
		default:
			return nil, fmt.Errorf("unknown adapter target %q", r.Into.Target)
		}
	}

	// Share a single full.Engine across all full-engine lenses — the engine
	// is stateless; per-rule state lives in the CompiledRule passed to UseFullEngine.
	fullEngine := full.New()

	// projectionRevision reads the current Core KV revision for an arbitrary
	// key. The actor-aggregate envelope uses it to populate
	// `projectedFromRevisions`. Errors and absent keys collapse to 0, which the
	// envelope drops (partial coverage is acceptable).
	projectionRevision := func(k string) uint64 {
		entry, err := coreKV.Get(context.Background(), k)
		if err != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	startPipeline := func(r *lens.Rule) {
		// A Secure Lens needs the Vault before anything else is built —
		// refusing here leaves no half-constructed state behind.
		if len(r.Into.SecureColumns) > 0 && vaultBackend == nil {
			logger.Error("secure lens requires a Vault backend — set LATTICE_VAULT_MASTER_KEK(_FILE); lens not activated",
				"lensId", r.ID, "canonicalName", r.CanonicalName)
			return
		}

		adpt, err := buildAdapter(r)
		if err != nil {
			logger.Error("build adapter", "lensId", r.ID, "err", err)
			return
		}

		reporter := health.New(healthKVHandle, r.ID)
		reporter.SetRuleSequence(r.Sequence)
		reporter.SetRuleEngine(r.ResolvedEngine)

		p, err := pipeline.New(r.ID, r.Into.Target, coreKVBucket, adjKV, coreKV, adpt, reporter)
		if err != nil {
			logger.Error("create pipeline", "lensId", r.ID, "err", err)
			return
		}

		// Wire full engine when selected.
		if r.ResolvedEngine == ruleengine.EngineFull {
			if r.CompiledRule == nil {
				logger.Error("full engine selected but CompiledRule is nil", "lensId", r.ID)
				return
			}
			// Thread the output key columns so the engine builds the complete
			// multi-column projection key (a composite-key lens — e.g. a
			// GrantTable lens — needs every key column the adapter requires).
			// Fail closed if a key column is not a RETURN alias. Only PLAIN
			// projection lenses are threaded: an envelope lens (actor-aggregate /
			// operation-role-index) derives its projection key from the envelope
			// at write time, so its Into.Key (e.g. ["key"]) is not a RETURN alias
			// and its applyReturn key map is discarded — threading/validating it
			// would wrongly fail activation. A fan-out Personal Lens
			// (projection.IsPersonalLens) is the same shape: its "__actor" key
			// field is injected by the envelope from the enumerated recipient, not
			// a RETURN alias, so InstallPersonalLens threads its business-only key
			// columns itself.
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok &&
				!projection.IsActorAggregate(r) && !isOperationRoleIndexLens(r) && !projection.IsPersonalLens(r) {
				cr.KeyColumns = []string(r.Into.Key)
				if err := cr.ValidateKeyColumns(); err != nil {
					logger.Error("full engine key-column validation", "lensId", r.ID, "err", err)
					return
				}
				// A Secure Lens's secure + identity-key columns must be RETURN
				// aliases — a typo would otherwise project silent nulls (secure
				// column) or Terminal-DLQ every row (identity-key column) with
				// nothing pointing at the misdeclared spec.
				if err := cr.ValidateReturnAliases(secureAliasNames(r.Into.SecureColumns)...); err != nil {
					logger.Error("secure-column RETURN-alias validation", "lensId", r.ID, "err", err)
					return
				}
			}
			p.UseFullEngine(fullEngine, r.CompiledRule)
		}

		// Fire 3 (negative-filter-retraction-projection-design.md §2.4): a plain
		// lens whose composite output key isn't derivable read-free from its own
		// anchor opts into target-diff retraction via the lens-definition flag —
		// data-driven, not canonical-name-keyed, same as every other per-lens
		// component below. Fail closed if the query isn't genuinely unanchored:
		// the diff compares the target's FULL live key set against the
		// re-execute's FULL freshly-computed row set, which is only exact when
		// that row set is already the complete global truth — an
		// $actorKey-scoped query would instead retract every OTHER live
		// anchor's rows on its first event.
		if r.Into.DiffRetraction {
			cr, ok := r.CompiledRule.(*full.CompiledRule)
			if !ok {
				logger.Error("diff retraction requires the full engine", "lensId", r.ID)
				return
			}
			if err := cr.ValidateUnanchoredForDiffRetraction(); err != nil {
				logger.Error("diff retraction validation", "lensId", r.ID, "err", err)
				return
			}
			p.SetDiffRetraction(true)
			logger.Info("diff retraction installed", "lensId", r.ID)
		}

		// Convergence-lens no-filtering-WHERE activation guard
		// (negative-filter-retraction-projection-design.md's review carry-out;
		// docs/components/refractor.md's authoring invariant). A plain
		// (non-actorAggregate) lens projecting into the shared weaver-targets
		// bucket must carry no filtering WHERE — Fire 2's presence-check
		// retraction would emit a Delete on a WHERE-dropped anchor, which
		// Weaver reads as "entity gone," not "stopped violating." actorAggregate
		// lenses (e.g. unroutedTasks) are exempt: their retraction runs through
		// the envelope's EmptyBehavior, not this path, so a filtering WHERE
		// there is safe and already shipped. Data-driven, not
		// canonical-name-keyed — a brand-new convergence lens is checked for
		// free. Simple-engine lenses have no CompiledRule of this shape and are
		// silently out of scope (they express matching differently).
		if r.Into.Target == "nats_kv" && r.Into.Bucket == bootstrap.WeaverTargetsBucket && !projection.IsActorAggregate(r) {
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok {
				if err := cr.ValidateNoFilteringWhereForConvergence(); err != nil {
					logger.Error("convergence-lens filtering-WHERE validation", "lensId", r.ID, "err", err)
					return
				}
			}
		}

		// Install the per-lens projection components via data-driven paths keyed
		// off lens-definition aspects — never off the canonical name. An
		// actor-aggregate lens (projectionKind: actorAggregate) is driven by the
		// compiled ProjectionPlan: the §6.13 Output descriptor shapes the on-wire
		// envelope, the cross-vertex fan-out, the empty/delete-key behavior, and the
		// guard predicate. A brand-new package lens that opts in flows through the
		// same path with zero edits here. The operation-aggregate role-index lens
		// (keyed by operationType) is driven by the generic null-key-skip envelope.
		switch {
		case projection.IsActorAggregate(r):
			if !projection.InstallActorAggregate(p, adpt, r, projectionRevision, adjKV, coreKV, logger) {
				return
			}
		case isOperationRoleIndexLens(r):
			// Operation-aggregate lens (the role-by-operation index): keyed by
			// operationType and targeting the capability-kv bucket, it rewrites
			// each row into the Contract #6 §6.1 `cap.role-by-operation.<op>`
			// shape and skips rows whose operationType is null/empty (a collect
			// over zero MATCH bindings). It is keyed by operationType, not by
			// actor — no per-actor revoke→resurrect race — so it is NOT guarded
			// (Contract #6 §6.2/§6.3). Routed off the operationType key plus the
			// capability-kv bucket, not a canonical name.
			p.SetEnvelopeFn(capabilityenv.NewRoleIndexWrapper())
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("operation-aggregate envelope installed", "lensId", r.ID, "key", r.Into.Key[0])
		case projection.IsPersonalLens(r):
			// Personal Lens fan-out (personal-secure-lens-design.md §3.3-3.4,
			// Fires PL.2-PL.3): installs the ActorEnumerator + the
			// "__actor"-injecting envelope, gated by D1's read-grant check
			// (capabilityKV) and filtered through the Interest Set registry.
			if !projection.InstallPersonalLens(p, r, adjKV, coreKV, personalInterestKV, capabilityKV, logger) {
				return
			}
			// The Hydration Hook (personal-secure-lens-design.md §3.5, Fire
			// PL.4): the "personal.hydrate" control RPC dispatches to this
			// lens's own pipeline. One Personal Lens per deployment, so this
			// is a single handle, not a per-ruleID registry.
			controlSvc.SetPersonalHydrator(p)
		}

		// A Secure Lens (Contract #3 §3.10): install the decrypt-at-projection
		// transform (the Vault-present check ran before the adapter was built).
		// translateSpec already guaranteed protected-postgres posture, so the
		// RLS verify-and-pause below applies.
		if len(r.Into.SecureColumns) > 0 {
			cols := make([]pipeline.SecureColumn, len(r.Into.SecureColumns))
			for i, sc := range r.Into.SecureColumns {
				cols[i] = pipeline.SecureColumn{Column: sc.Column, IdentityKeyColumn: sc.IdentityKeyColumn, Field: sc.Field}
			}
			dec, err := pipeline.NewSecureDecryptor(vaultBackend, coreKV, cols, &vaultCalls)
			if err != nil {
				logger.Error("build secure decryptor", "lensId", r.ID, "err", err)
				return
			}
			p.SetSecureDecryptor(dec)
			logger.Info("secure lens decryptor installed", "lensId", r.ID, "columns", len(cols))
		}

		// Configure the supervised runtime: durable name refractor-<ruleID>,
		// queue group = same name (NFR12), DeliverLastPerSubject (ADR-15), Core
		// KV stream + filter. The supervisor creates the durable idempotently when
		// Run registers it (CreateOrUpdateConsumer). ruleID must not be "adjacency"
		// (collides with the bootstrapper's refractor-adjacency consumer).
		// A protected/grant Postgres lens starts infra-paused so its Probe verifies
		// the out-of-band RLS posture BEFORE the first projection — fail-closed
		// (Contract #6 §6.14, verify-and-pause). Every other lens drains
		// immediately (zero-value InitialPause).
		var initialPause substrate.PauseReason
		if r.Into.Protected || r.Into.GrantTable {
			initialPause = substrate.PauseInfra
		}
		p.RunOn(conn, substrate.ConsumerSpec{
			Name:          "refractor-" + r.ID,
			Stream:        subjects.CoreKVStream(coreKVBucket),
			FilterSubject: subjects.CoreKVFilter(coreKVBucket),
			DeliverPolicy: substrate.DeliverLastPerSubject,
			DeliverGroup:  "refractor-" + r.ID,
			InitialPause:  initialPause,
		})

		// Per-lens lag metrics: read pending from the supervised consumer by
		// durable name, so the poller tracks the live consumer across a rebuild
		// reset with no handle re-binding.
		lp := health.NewLagPoller(conn, p.Pending, reporter, r.ID)
		lp.SetProgressFunc(func() time.Time { return p.Progress().LastProjectedAt })
		p.SetLagPoller(lp)

		// Transient write failures escalate to the shared retry queue (deferred
		// backoff, then DLQ on exhaustion) when the rule declares one; absent
		// `retry:`, a Transient failure keeps Naking for redelivery as before.
		if r.Retry.MaxAttempts > 0 {
			backoff, err := failure.ParseISO8601Duration(r.Retry.Backoff)
			if err != nil {
				logger.Error("parse retry backoff", "lensId", r.ID, "backoff", r.Retry.Backoff, "err", err)
				return
			}
			p.SetRetryQueue(retryQueue, conn, r.Retry.MaxAttempts, backoff)
		}

		// Per-rule audit trail: append an entry to lattice.refractor.audit.<lensId>
		// on every successful write (docs/components/refractor-failure-tiers.md).
		aw := health.NewAuditWriter(conn, r.ID)
		if err := aw.EnsureStream(ctx); err != nil {
			logger.Error("ensure audit stream", "lensId", r.ID, "err", err)
			return
		}
		p.SetAuditWriter(aw)

		lensCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})

		mu.Lock()
		registry[r.ID] = &pipelineEntry{
			cancel:        cancel,
			done:          done,
			pipeline:      p,
			reporter:      reporter,
			canonicalName: r.CanonicalName,
			authPlane:     projection.IsAuthPlane(r),
			secureColumns: r.Into.SecureColumns,
		}
		mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			p.Run(lensCtx)
		}()

		controlSvc.Register(r.ID, p, reporter)
		controlSvc.RegisterPauser(r.ID, p)
		controlSvc.RegisterRebuilder(r.ID, p)
		controlSvc.RegisterRowNullifier(r.ID, p)

		logger.Info("lens pipeline started", "lensId", r.ID, "target", r.Into.Target, "table", r.Into.Table, "bucket", r.Into.Bucket)
	}

	updateCB := func(old, newLens *lens.Rule, kind lens.UpdateKind) {
		switch kind {
		case lens.IntoOnly:
			mu.Lock()
			entry, ok := registry[newLens.ID]
			mu.Unlock()
			if !ok {
				logger.Warn("update on unknown lens", "lensId", newLens.ID)
				return
			}
			// A live pipeline's secure decryptor is fixed at activation (installing
			// one mid-run would race the handler); changing a lens's secureColumns
			// requires a lens re-create, so refuse the hot-reload loudly rather
			// than swap the adapter and leave the decrypt set stale. The baseline
			// is the RUNNING pipeline's activated set (entry.secureColumns), not
			// the last-seen spec — a refused update must not poison later
			// comparisons. A secure lens also refuses table/DSN swaps: hot-reload
			// has no verify-and-pause, so the new target's RLS posture would be
			// unprobed while the rows carry decrypted PII.
			if !secureColumnsEqual(entry.secureColumns, newLens.Into.SecureColumns) {
				logger.Error("lens INTO update changes secureColumns — not hot-reloadable; delete and re-create the lens",
					"lensId", newLens.ID)
				return
			}
			if len(entry.secureColumns) > 0 &&
				(old.Into.Table != newLens.Into.Table || old.Into.DSN != newLens.Into.DSN) {
				logger.Error("secure lens INTO update changes table/dsn — not hot-reloadable (no RLS re-verify on swap); delete and re-create the lens",
					"lensId", newLens.ID)
				return
			}
			newAdpt, err := buildAdapter(newLens)
			if err != nil {
				logger.Error("build new adapter", "lensId", newLens.ID, "err", err)
				return
			}
			if err := entry.pipeline.HotReloadInto(newAdpt); err != nil {
				logger.Error("hot-reload adapter", "lensId", newLens.ID, "err", err)
				return
			}
			entry.reporter.SetRuleSequence(newLens.Sequence)
			entry.reporter.SetRuleEngine(newLens.ResolvedEngine)
			logger.Info("lens INTO hot-reloaded", "lensId", newLens.ID)
		case lens.MatchChange:
			mu.Lock()
			entry, ok := registry[newLens.ID]
			mu.Unlock()
			if !ok {
				logger.Warn("MATCH update on unknown lens", "lensId", newLens.ID)
				return
			}
			// ClassifyUpdate keys on the Match string alone, so a single update
			// changing the cypher AND secureColumns lands here — the running
			// decryptor's column set must still be the activated one, and refused
			// updates compare against it (never against a refused spec).
			if !secureColumnsEqual(entry.secureColumns, newLens.Into.SecureColumns) {
				logger.Error("lens MATCH update changes secureColumns — not hot-reloadable; delete and re-create the lens",
					"lensId", newLens.ID)
				return
			}
			// CoreKVSource has already compiled the new rule; reuse it.
			if newLens.CompiledRule == nil {
				logger.Error("MATCH update missing CompiledRule",
					"lensId", newLens.ID)
				return
			}
			if cr, ok := newLens.CompiledRule.(*full.CompiledRule); ok &&
				!projection.IsActorAggregate(newLens) && !isOperationRoleIndexLens(newLens) {
				cr.KeyColumns = []string(newLens.Into.Key)
				if err := cr.ValidateKeyColumns(); err != nil {
					logger.Error("full engine key-column validation (MATCH update)",
						"lensId", newLens.ID, "err", err)
					return
				}
				// The new cypher must still RETURN every alias the running
				// decryptor consumes.
				if err := cr.ValidateReturnAliases(secureAliasNames(entry.secureColumns)...); err != nil {
					logger.Error("secure-column RETURN-alias validation (MATCH update)",
						"lensId", newLens.ID, "err", err)
					return
				}
			}
			entry.pipeline.UseFullEngine(fullEngine, newLens.CompiledRule)
			logger.Info("lens MATCH hot-reloaded", "lensId", newLens.ID)
			entry.reporter.SetRuleSequence(newLens.Sequence)
			entry.reporter.SetRuleEngine(newLens.ResolvedEngine)
		}
	}

	// Wait for adjacency bootstrap before activating any lens.
	select {
	case <-bootstrapper.Ready():
		logger.Info("adjacency bootstrap complete")
	case <-ctx.Done():
		return
	}

	// Source 1: Core KV watch on `vtx.meta.>`, routed by envelope class
	// `meta.lens` (Decision #5; data-contracts.md §1.2 line 70).
	src := lens.NewCoreKVSource(conn, coreKVBucket, logger)
	src.SetLoadCallback(func(r *lens.Rule) {
		mu.Lock()
		_, exists := registry[r.ID]
		mu.Unlock()
		if !exists {
			startPipeline(r)
		}
	})
	src.SetUpdateCallback(updateCB)
	controlSvc.SetRuleGetter(src)
	if err := src.Start(ctx); err != nil {
		logger.Error("start core kv lens source", "err", err)
		os.Exit(1)
	}
	logger.Info("core kv lens source started", "watchPrefix", "vtx.meta.>", "classFilter", "meta.lens")

	// Bootstrap lens (env-gated). Activates only if no meta-lens has loaded
	// after a short grace window AND the env var is set. Decision #7.
	if lens.BootstrapEnabled() {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			mu.Lock()
			n := len(registry)
			mu.Unlock()
			if n == 0 {
				logger.Info("activating hardcoded bootstrap lens (REFRACTOR_BOOTSTRAP_LENS set, no meta-lens present)")
				startPipeline(lens.BootstrapLens())
			}
		}()
	}

	if err := controlSvc.StartNATSListener(ctx, nc); err != nil {
		logger.Error("start control NATS listener", "err", err)
		os.Exit(1)
	}
	logger.Info("control service started")

	logger.Info("refractor ready", "instance", instance)
	<-ctx.Done()
	logger.Info("refractor shutting down")
	wg.Wait()
	poolManager.Close()
}

// isOperationRoleIndexLens reports whether r is the operation-aggregate
// role-by-operation index: its sole output key is operationType AND it
// targets the capability-kv bucket (Contract #6 §6.1). Both conditions are
// required — a package lens keyed solely by operationType but targeting a
// different bucket does not match, and is left to its default envelope.
// Derived from the lens's Into descriptor, never from a canonical name.
func isOperationRoleIndexLens(r *lens.Rule) bool {
	return len(r.Into.Key) == 1 && r.Into.Key[0] == "operationType" && projection.IsAuthPlane(r)
}

// loadVault wires the optional local envelope-encryption Vault backend for
// Secure-Lens decrypt-at-projection. The master KEK is read from
// LATTICE_VAULT_MASTER_KEK (inline base64) if set, else from the file at
// LATTICE_VAULT_MASTER_KEK_FILE — the same sources cmd/processor uses, so one
// provisioned KEK (make provision-vault-kek) serves both processes. Unlike
// the Processor (which refuses to start without a KEK — it would otherwise
// commit plaintext), neither being set is fine here: (nil, nil), and any
// Secure Lens fails closed at activation instead.
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
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load vault master KEK: %w", err)
	}
	v, err := vault.NewLocalBackend(kek, envOr("LATTICE_VAULT_KEK_VERSION", ""))
	if err != nil {
		return nil, fmt.Errorf("construct vault backend: %w", err)
	}
	logger.Info("vault wired for secure lenses", "backend", "local")
	return v, nil
}

// secureColumnsEqual reports whether two secure-column declarations are
// identical (order-sensitive — the spec is authored, not computed).
func secureColumnsEqual(a, b []lens.SecureColumn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// secureAliasNames collects every RETURN alias a secure-column declaration
// consumes (the ciphertext column + its identity-key column, deduplicated).
func secureAliasNames(cols []lens.SecureColumn) []string {
	if len(cols) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cols)*2)
	out := make([]string, 0, len(cols)*2)
	for _, sc := range cols {
		for _, n := range []string{sc.Column, sc.IdentityKeyColumn} {
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	mode := controlauth.AuthMode(envOr("LATTICE_AUTH_MODE", string(controlauth.AuthModeCapability)))
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

	alerts := controlauth.NewHealthAlertEmitter(conn, healthKVBucket, logger)
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

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
