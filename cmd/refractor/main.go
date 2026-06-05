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

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
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
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: *natsURL})
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

	// Open Core KV and the (pre-provisioned) refractor-adjacency bucket.
	coreKV, err := js.KeyValue(ctx, coreKVBucket)
	if err != nil {
		logger.Error("open core KV", "bucket", coreKVBucket, "err", err)
		os.Exit(1)
	}
	adjKV, err := js.KeyValue(ctx, adjacencyKVBucket)
	if err != nil {
		logger.Error("open refractor adjacency KV", "bucket", adjacencyKVBucket, "err", err)
		os.Exit(1)
	}
	healthKVHandle, err := js.KeyValue(ctx, healthKVBucket)
	if err != nil {
		logger.Error("open health KV", "bucket", healthKVBucket, "err", err)
		os.Exit(1)
	}

	bootstrapper := consumer.NewBootstrapper(js, coreKVBucket, adjKV)
	go func() {
		if err := bootstrapper.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("adjacency bootstrap failed — no lenses will start", "err", err)
			stop() // cancel the root context so main exits and the process can restart
		}
	}()

	manager := consumer.NewManager(js, coreKVBucket)
	poolManager := adapter.NewPoolManager()
	controlSvc := control.NewService()
	controlSvc.SetCoreKV(coreKV)

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

	// LagProvider for the heartbeater — read consumer NumPending per lens.
	hb.LagProvider = func() map[string]uint64 {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]uint64, len(registry))
		for lensID := range registry {
			cons := manager.Consumer(lensID)
			if cons == nil {
				continue
			}
			info, err := cons.Info(context.Background())
			if err == nil && info != nil {
				out[lensID] = info.NumPending
			}
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
			// Try Open before Create so pre-provisioned buckets (e.g. capability-kv)
			// are reused instead of failing with "bucket name already in use".
			targetKV, err := js.KeyValue(ctx, r.Into.Bucket)
			if err != nil {
				targetKV, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: r.Into.Bucket})
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
			// The adapter is thin: it upserts into a pre-existing table.
			// Target tables are provisioned out-of-band (the adapter and
			// Refractor never issue table DDL) — see
			// docs/components/refractor.md.
			return adapter.NewPostgresAdapter(pool, r.Into.Table, r.Into.Key, r.Into.QueryTimeout, deleteMode)
		default:
			return nil, fmt.Errorf("unknown adapter target %q", r.Into.Target)
		}
	}

	// Share a single full.Engine across all full-engine lenses — the engine
	// is stateless; per-rule state lives in the CompiledRule passed to UseFullEngine.
	fullEngine := full.New()

	// projectionRevisionFn reads the current Core KV revision for an
	// arbitrary key. Used by the Capability envelope wrapper to populate
	// `projectedFromRevisions`. Errors and absent keys collapse to 0,
	// which the envelope drops (partial coverage is acceptable).
	projectionRevision := func(k string) uint64 {
		entry, err := coreKV.Get(context.Background(), k)
		if err != nil || entry == nil {
			return 0
		}
		return entry.Revision()
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
		p.SetConsumerResetter(manager)

		// Wire full engine when selected.
		if r.ResolvedEngine == ruleengine.EngineFull {
			if r.CompiledRule == nil {
				logger.Error("full engine selected but CompiledRule is nil", "lensId", r.ID)
				return
			}
			p.UseFullEngine(fullEngine, r.CompiledRule)
		}

		// Install per-lens envelope + fan-out + latency components. The canonical
		// name is the only stable identifier between a seeded LensDefinition and
		// the runtime Rule.
		switch r.CanonicalName {
		case "capability":
			lensDefKey := "vtx.meta." + r.ID
			// Capability KV envelope rewrites each projection row into the
			// Contract #6 §6.2 per-actor shape keyed at cap.identity.<id>.
			p.SetEnvelopeFn(capabilityenv.NewWrapper(lensDefKey, projectionRevision))
			// Cross-vertex fan-out: non-identity CDC events are expanded into the
			// set of affected actors via adjacency BFS (depth + actor-set caps).
			p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))
			// Per-Lens latency ring buffer for heartbeat NFR-P3 emission.
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("capability envelope + fan-out + latency installed",
				"lensId", r.ID, "lensDefKey", lensDefKey)
		case "capabilityRoleIndex":
			// The role-index envelope rewrites each row into the Contract #6 §6.1
			// `cap.role-by-operation.<op>` shape and skips rows whose
			// operationType is null/empty. capabilityRoleIndex does not use the
			// per-actor envelope.
			p.SetEnvelopeFn(capabilityenv.NewRoleIndexWrapper())
			// Latency buffer also installed for the secondary Lens — the
			// heartbeater emits stats per Lens regardless of envelope shape.
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("capabilityRoleIndex envelope installed", "lensId", r.ID)
		case "capabilityEphemeral":
			// The orchestration-base ephemeral-grant lens (Contract #6 §6.6
			// amendment / Contract #10 §10.7). It projects FR56 grants to the
			// DISJOINT key `cap.ephemeral.<actor>` in the shared
			// capability-kv bucket. Like the primary capability lens it needs
			// cross-vertex fan-out: a CDC event on a task / link must
			// re-project the affected actor(s) rather than only direct
			// identity events.
			lensDefKey := "vtx.meta." + r.ID
			p.SetEnvelopeFn(capabilityenv.NewEphemeralWrapper(lensDefKey, projectionRevision))
			p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))
			// Actor disappearance must delete this lens's disjoint
			// cap.ephemeral.<actor> key, not the primary cap.<actor> doc.
			p.SetActorDeleteKey(capabilityenv.EphemeralKey)
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("capabilityEphemeral envelope + fan-out + latency installed",
				"lensId", r.ID, "lensDefKey", lensDefKey)
		case "myTasks":
			// The orchestration-base my-tasks lens (Contract #10 §10.1). It
			// projects, per identity, that identity's OPEN tasks into the
			// package-owned my-tasks bucket keyed my-tasks.identity.<id>. Like
			// the ephemeral lens it is link-sourced + needs cross-vertex
			// fan-out: a CDC event on a task root (open→closed) or an
			// assignedTo link (reassign) must reproject the affected actor(s),
			// and zero open tasks must hard-delete the key (vanish-on-close).
			lensDefKey := "vtx.meta." + r.ID
			p.SetEnvelopeFn(capabilityenv.NewMyTasksWrapper(lensDefKey, projectionRevision))
			p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))
			// Actor disappearance must delete this lens's my-tasks.identity.<id>
			// key, not the primary cap.<actor> doc.
			p.SetActorDeleteKey(capabilityenv.MyTasksKey)
			p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))
			logger.Info("myTasks envelope + fan-out + latency installed",
				"lensId", r.ID, "lensDefKey", lensDefKey)
		}

		if err := manager.Add(ctx, r.ID); err != nil {
			logger.Error("manager add consumer", "lensId", r.ID, "err", err)
			return
		}
		cons := manager.Consumer(r.ID)

		lp := health.NewLagPoller(nc, cons, reporter, r.ID)
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
		}
		mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			p.Run(lensCtx, cons)
		}()

		controlSvc.Register(r.ID, p, reporter)
		controlSvc.RegisterPauser(r.ID, p)
		controlSvc.RegisterRebuilder(r.ID, p)

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
