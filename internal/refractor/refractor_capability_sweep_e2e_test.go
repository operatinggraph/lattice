// Auth-plane convergence sweep e2e: the periodic self-audit finds and heals a
// capability document a lost CDC event never projected — without anyone naming
// the affected actor (capability-projection-reconciliation-design.md §3.2).
//
// This is the half that removes the class rather than repairing one instance.
// The verb (§3.1, its own e2e) needs an operator who already knows which actor
// is dark; the observed incident had nobody who knew. Here the pipeline's
// consumer is never started, so the grant below is never consumed — exactly
// the availability gap of §2.2 — and the only thing running is the sweep.
package refractor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func TestRefractor_ConvergenceSweep_DetectsAndHealsLostProjection_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping convergence-sweep e2e test in -short mode")
	}

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(ctx, "health-kv")
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test-sweep", logger)
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	timeout := time.After(10 * time.Second)
	var capabilityRule *lens.Rule
	for capabilityRule == nil {
		select {
		case r := <-loaded:
			if r.CanonicalName == "capability" {
				capabilityRule = r
			}
		case <-timeout:
			t.Fatal("did not activate capability lens within 10s")
		}
	}
	require.Equal(t, ruleengine.EngineFull, capabilityRule.ResolvedEngine)

	targetKV, err := conn.OpenKV(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	adpt, err := adapter.New(targetKV, capabilityRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	reporter := health.New(healthKV, capabilityRule.ID)
	p, err := pipeline.New(capabilityRule.ID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), capabilityRule.CompiledRule)
	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	capDesc, err := projection.ParseOutputDescriptor(capabilityRule.Output)
	require.NoError(t, err)
	p.SetEnvelopeFn(capDesc.EnvelopeFn("vtx.meta."+capabilityRule.ID, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capDesc.AnchorType))
	p.SetActorDeleteKey(capDesc.BuildKey)

	// The sweep as the driver installs it, with the tick compressed so the test
	// exercises many bounded passes rather than waiting out the production
	// minute. Batch and interval are the only things it overrides.
	p.SetSweepPlan(pipeline.SweepPlan{
		AnchorType:    capDesc.AnchorType,
		BuildKey:      capDesc.BuildKey,
		AnchorFromKey: capDesc.AnchorFromKey,
		Interval:      250 * time.Millisecond,
		Batch:         25,
	})

	// The graph the pipeline never saw: written straight to Core KV with no
	// lens consumer running, so no CDC event for it is ever applied.
	identityID := stableNanoID("sweep-alice")
	identityKey := substrate.VertexKey("identity", identityID)
	provenanceAt := "2026-07-22T10:00:00Z"

	body := map[string]any{
		"key": identityKey, "class": "identity",
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"name": "sweep-alice", "protected": true},
	}
	data, jerr := json.Marshal(body)
	require.NoError(t, jerr)
	_, err = coreKV.Put(ctx, identityKey, data)
	require.NoError(t, err)

	holdsRoleLinkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", bootstrap.RoleOperatorID)
	linkBody, lerr := bootstrap.MakeLinkEnvelope(holdsRoleLinkKey, identityKey, bootstrap.RoleOperatorKey, "holdsRole", "holdsRole", nil)
	require.NoError(t, lerr)
	_, err = coreKV.Put(ctx, holdsRoleLinkKey, linkBody)
	require.NoError(t, err)

	// The reprojection cypher walks adjacency, built by the bootstrapper's own
	// consumer — independent of the lens pipeline, which is why the graph can
	// be complete while the capability document is missing.
	adjDeadline := time.Now().Add(15 * time.Second)
	for {
		entry, gerr := adjKV.Get(ctx, subjects.AdjKey(identityID))
		if gerr == nil && entry != nil && bytes.Contains(entry.Value, []byte("holdsRole")) {
			break
		}
		if time.Now().After(adjDeadline) {
			t.Fatal("adjacency edge for the holdsRole link did not appear within 15s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	expectedKey := capDesc.BuildKey(identityKey)
	entry, err := capabilityKV.Get(ctx, expectedKey)
	require.True(t, err != nil || entry == nil || len(entry.Value) == 0,
		"capability doc must be absent before the sweep runs (the lost-event state)")

	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	swept := make(chan struct{})
	go func() { defer close(swept); p.RunSweep(sweepCtx) }()

	// Detection: nobody told the sweep which actor was dark. It compared the
	// lens's anchors against the target's live keys and found the hole itself.
	healDeadline := time.Now().Add(30 * time.Second)
	var healed *substrate.KVEntry
	for {
		e, gerr := capabilityKV.Get(ctx, expectedKey)
		if gerr == nil && e != nil && len(e.Value) > 0 {
			healed = e
			break
		}
		if time.Now().After(healDeadline) {
			t.Fatal("the convergence sweep did not heal the missing capability doc within 30s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var env map[string]any
	require.NoError(t, json.Unmarshal(healed.Value, &env))
	require.Equal(t, expectedKey, env["key"])
	require.Equal(t, identityKey, env["actor"])

	sw := p.Sweeper()
	require.NotNil(t, sw)
	require.GreaterOrEqual(t, sw.Status().Reconciled, uint64(1),
		"a healed divergence must be counted so the heal is loud, not silent")

	// Convergence: once the world agrees, the sweep must go quiet — the
	// divergent streak clears (which is what closes CapabilityCoverageDivergence)
	// and no further write touches the healed row.
	quietDeadline := time.Now().Add(30 * time.Second)
	for sw.Status().DivergentStreak != 0 {
		if time.Now().After(quietDeadline) {
			t.Fatalf("the sweep never reported a clean pass; streak=%d reconciled=%d",
				sw.Status().DivergentStreak, sw.Status().Reconciled)
		}
		time.Sleep(100 * time.Millisecond)
	}

	settled, err := capabilityKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	reconciledAtRest := sw.Status().Reconciled

	// Several more ticks against a converged world: skip-if-identical must make
	// them cost zero writes, or the sweep would churn the auth plane forever.
	time.Sleep(2 * time.Second)
	after, err := capabilityKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	require.Equal(t, settled.Revision, after.Revision,
		"a converged sweep must not rewrite the row")
	require.Equal(t, reconciledAtRest, sw.Status().Reconciled,
		"a converged sweep must not count phantom heals")
	require.Zero(t, sw.Status().DivergentStreak)

	// The cursor and heal count live on the lens's existing health entry, so a
	// restarted process resumes the round-robin walk instead of restarting it.
	persisted, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, persisted.SweepCursor, "the round-robin cursor must be persisted")
	require.Equal(t, reconciledAtRest, persisted.SweepReconciled)

	stopSweep()
	select {
	case <-swept:
	case <-time.After(10 * time.Second):
		t.Fatal("RunSweep did not stop with its context")
	}
}
