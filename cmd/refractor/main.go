// refractor is the Lattice projection engine — the lift-and-shift of
// Materializer's Stream 2 pipeline, adapted to consume Core KV CDC and
// source lens definitions from `vtx.meta.>` (filtered by envelope
// class `meta.lens` per data-contracts.md §1.2 line 70). Story 2.1.
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
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
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
	cancel   context.CancelFunc
	done     chan struct{}
	pipeline *pipeline.Pipeline
	reporter *health.Reporter
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
	go func() { _ = bootstrapper.Run(ctx) }()

	manager := consumer.NewManager(js, coreKVBucket)
	poolManager := adapter.NewPoolManager()
	controlSvc := control.NewService()
	controlSvc.SetCoreKV(coreKV)

	var (
		mu       sync.Mutex
		registry = make(map[string]*pipelineEntry)
		wg       sync.WaitGroup
	)

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
		switch r.Into.Target {
		case "nats_kv":
			targetKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: r.Into.Bucket})
			if err != nil {
				return nil, err
			}
			return adapter.New(targetKV, r.Into.Key)
		case "postgres":
			pool, err := poolManager.Acquire(ctx, r.Into.DSN)
			if err != nil {
				return nil, err
			}
			// Ensure the target table exists with soft-delete columns. Story 2.1:
			// idempotent CREATE IF NOT EXISTS keeps the bootstrap lens runnable.
			if err := ensurePostgresTable(ctx, pool, r.Into.Table, r.Into.Key); err != nil {
				return nil, err
			}
			return adapter.NewPostgresAdapter(pool, r.Into.Table, r.Into.Key, r.Into.QueryTimeout)
		default:
			return nil, fmt.Errorf("unknown adapter target %q", r.Into.Target)
		}
	}

	startPipeline := func(r *lens.Rule) {
		q, err := simple.Parse(r.Match)
		if err != nil {
			logger.Error("parse lens cypher", "lensId", r.ID, "err", err)
			return
		}
		plan, err := simple.Compile(q, r.Into.Key)
		if err != nil {
			logger.Error("compile lens query plan", "lensId", r.ID, "err", err)
			return
		}
		adpt, err := buildAdapter(r)
		if err != nil {
			logger.Error("build adapter", "lensId", r.ID, "err", err)
			return
		}

		reporter := health.New(healthKVHandle, r.ID, r.Team)
		reporter.SetRuleSequence(r.Sequence)
		reporter.SetRuleEngine(r.ResolvedEngine)

		p, err := pipeline.New(r.ID, r.Team, r.Into.Target, plan, coreKVBucket, adjKV, coreKV, adpt, reporter)
		if err != nil {
			logger.Error("create pipeline", "lensId", r.ID, "err", err)
			return
		}
		p.SetConsumerResetter(manager)

		if err := manager.Add(ctx, r.ID); err != nil {
			logger.Error("manager add consumer", "lensId", r.ID, "err", err)
			return
		}
		cons := manager.Consumer(r.ID)

		lp := health.NewLagPoller(nc, cons, reporter, r.ID, r.Team)
		p.SetLagPoller(lp)

		lensCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})

		mu.Lock()
		registry[r.ID] = &pipelineEntry{cancel: cancel, done: done, pipeline: p, reporter: reporter}
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
			mu.Lock()
			entry, ok := registry[newLens.ID]
			mu.Unlock()
			if ok {
				if err := entry.pipeline.HotReloadPlan(newPlan); err != nil {
					logger.Error("hot-reload plan", "lensId", newLens.ID, "err", err)
				} else {
					logger.Info("lens MATCH hot-reloaded", "lensId", newLens.ID)
				}
				entry.reporter.SetRuleSequence(newLens.Sequence)
				entry.reporter.SetRuleEngine(newLens.ResolvedEngine)
			}
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
