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
	"syscall"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/keyshredded"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	coreKVBucket          = "core-kv"
	healthKVBucket        = "health-kv"
	adjacencyKVBucket     = "refractor-adjacency"
	defaultHeartbeatEvery = 10 * time.Second
)

type pipelineEntry struct {
	cancel        context.CancelFunc
	done          chan struct{}
	pipeline      *pipeline.Pipeline
	reporter      *health.Reporter
	canonicalName string // keyed under lensLatency in heartbeats.
	authPlane     bool   // projects the capability-kv authorization surface (projection.IsAuthPlane).
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

	bootstrapper := consumer.NewBootstrapper(conn, coreKVBucket, adjKV)
	go func() {
		if err := bootstrapper.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("adjacency bootstrap failed — no lenses will start", "err", err)
			stop() // cancel the root context so main exits and the process can restart
		}
	}()

	poolManager := adapter.NewPoolManager()
	controlSvc := control.NewService()
	controlSvc.SetCoreKV(coreKV)

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
	hb.KeyShreddedHandledTotalProvider = keyShredded.HandledTotal
	hb.VaultCallsTotalProvider = func() uint64 { return 0 } // Phase-1 stub — see field doc
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

	buildAdapter := func(r *lens.Rule) (adapter.Adapter, error) {
		// DeleteMode is defaulted to "hard" and validated upstream (Parse /
		// translateSpec); re-parse here to obtain the typed value for the adapter.
		deleteMode, err := adapter.ParseDeleteMode(r.Into.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: delete_mode: %w", r.ID, err)
		}
		switch r.Into.Target {
		case "nats_kv":
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
		// For the simple engine we still compile the plan (legacy path).
		// For the full engine we only need a placeholder — evaluateForEntry
		// switches on engineKind and never touches the plan.
		var plan *simple.QueryPlan
		if r.ResolvedEngine == ruleengine.EngineSimple || r.ResolvedEngine == "" {
			q, err := simple.Parse(r.Match)
			if err != nil {
				logger.Error("parse lens cypher", "lensId", r.ID, "err", err)
				return
			}
			plan, err = simple.Compile(q, r.Into.Key)
			if err != nil {
				logger.Error("compile lens query plan", "lensId", r.ID, "err", err)
				return
			}
		}
		adpt, err := buildAdapter(r)
		if err != nil {
			logger.Error("build adapter", "lensId", r.ID, "err", err)
			return
		}

		reporter := health.New(healthKVHandle, r.ID)
		reporter.SetRuleSequence(r.Sequence)
		reporter.SetRuleEngine(r.ResolvedEngine)

		p, err := pipeline.New(r.ID, r.Into.Target, plan, coreKVBucket, adjKV, coreKV, adpt, reporter)
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
			// would wrongly fail activation.
			if cr, ok := r.CompiledRule.(*full.CompiledRule); ok &&
				!projection.IsActorAggregate(r) && !isOperationRoleIndexLens(r) {
				cr.KeyColumns = []string(r.Into.Key)
				if err := cr.ValidateKeyColumns(); err != nil {
					logger.Error("full engine key-column validation", "lensId", r.ID, "err", err)
					return
				}
			}
			p.UseFullEngine(fullEngine, r.CompiledRule)
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
		p.SetLagPoller(lp)

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
			// Mirror startPipeline's per-engine routing for hot-reload so both
			// simple- and full-engine lenses are updated when MATCH changes.
			mu.Lock()
			entry, ok := registry[newLens.ID]
			mu.Unlock()
			if !ok {
				logger.Warn("MATCH update on unknown lens", "lensId", newLens.ID)
				return
			}
			switch newLens.ResolvedEngine {
			case ruleengine.EngineFull:
				// CoreKVSource has already compiled the new rule; reuse it.
				if newLens.CompiledRule == nil {
					logger.Error("full engine MATCH update missing CompiledRule",
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
				}
				entry.pipeline.UseFullEngine(fullEngine, newLens.CompiledRule)
				logger.Info("lens MATCH hot-reloaded (full engine)", "lensId", newLens.ID)
			default:
				q, err := simple.Parse(newLens.Match)
				if err != nil {
					logger.Error("parse updated match", "lensId", newLens.ID, "err", err)
					return
				}
				newPlan, err := simple.Compile(q, newLens.Into.Key)
				if err != nil {
					logger.Error("compile updated plan", "lensId", newLens.ID, "err", err)
					return
				}
				if err := entry.pipeline.HotReloadPlan(newPlan); err != nil {
					logger.Error("hot-reload plan", "lensId", newLens.ID, "err", err)
				} else {
					logger.Info("lens MATCH hot-reloaded (simple engine)", "lensId", newLens.ID)
				}
			}
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
