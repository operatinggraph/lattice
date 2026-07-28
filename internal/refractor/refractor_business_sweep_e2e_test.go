// Business-lens convergence sweep e2e: the healer that used to belong to the
// auth plane alone now runs for any actor-aggregate lens that can prove it owns
// its rows, and this proves the whole chain for one — enrolled, scoped, healed,
// siblings untouched — against a real substrate.
//
// The auth-plane equivalent (refractor_capability_sweep_e2e_test.go) covers the
// same chain for a lens whose target it owns outright. What that cannot reach is
// the property enrolment actually turned on: a business lens shares its bucket
// with a dozen others, so its sweep must heal its own hole without touching a
// sibling's rows. The pipeline-level tests cover the plane-independent path with
// a fake whose HasPrefix is subtly more permissive than the subject filter it
// stands in for, which is exactly why this one runs against real NATS KV.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// businessSweepBucket stands in for weaver-targets: one bucket, several lenses'
// rows. The sibling keys below are deliberately NOT under the lens's prefix.
const businessSweepBucket = "business-sweep-targets"

// businessLensRule is an actor-aggregate lens on a shared, non-auth-plane
// bucket. emptyBehavior delete is what makes it guarded (and therefore
// enrollable) without being on the auth plane — the combination the enrolment
// gate was widened to admit.
func businessLensRule(t *testing.T) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
RETURN identity.key AS actorKey
`)
	require.NoError(t, err)
	return &lens.Rule{
		ID:             "lens-business-sweep-e2e",
		CanonicalName:  "businessSweep",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: businessSweepBucket,
			Key:    lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "businessSweep.{actorSuffix}",
			BodyColumns:      []string{"actorKey"},
			EmptyBehavior:    string(projection.EmptyDelete),
			Freshness:        "auto",
		},
	}
}

func TestRefractor_BusinessLensConvergenceSweep_HealsWithoutTouchingSiblings_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping business-lens convergence-sweep e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
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
	healthKV, err := conn.OpenKV(ctx, "health-kv")
	require.NoError(t, err)
	// The shared business target is a package-provisioned bucket in production;
	// here it is created directly, the way the sibling actor-aggregate e2es do.
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: businessSweepBucket})
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, businessSweepBucket)
	require.NoError(t, err)

	rule := businessLensRule(t)
	adpt, err := adapter.New(targetKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	reporter := health.New(healthKV, rule.ID)
	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	// Enrolment through the REAL install gate, not a hand-built plan: what is
	// under test includes the decision that this lens may sweep at all.
	require.True(t, projection.InstallActorAggregate(p, adpt, rule, projectionRevision, adjKV, coreKV, logger),
		"a guarded actor-aggregate lens that can name its own keys must install")

	sw := p.Sweeper()
	require.NotNil(t, sw, "a business actor-aggregate lens must be enrolled in the convergence sweep")
	require.Equal(t, 5*time.Minute, sw.Interval(),
		"a business lens sweeps on the slower clock — a stale business read model is a vertical's outage, not an authorization failure")

	// Re-install the plan with the tick compressed, so the test exercises many
	// bounded passes rather than waiting out the business interval. Everything
	// else is what the driver just derived.
	desc, err := projection.ParseOutputDescriptor(rule.Output)
	require.NoError(t, err)
	prefix, ok := desc.KeyPrefix()
	require.True(t, ok)
	p.SetSweepPlan(pipeline.SweepPlan{
		AnchorType:    desc.AnchorType,
		AnchorFromKey: desc.AnchorFromKey,
		KeyPrefix:     prefix,
		Interval:      250 * time.Millisecond,
		Batch:         25,
	})
	sw = p.Sweeper()

	// A sibling lens's rows, in the same bucket, outside this lens's prefix.
	// Their revisions are the assertion: the sweep must not write them at all.
	siblings := map[string][]byte{
		"otherLens.identity.Hj4kPmRtw9nbCxz5vQ2y": []byte(`{"key":"otherLens.identity.Hj4kPmRtw9nbCxz5vQ2y","actor":"vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"}`),
		"thirdLens.identity.Lk2Pn6mQrtwzKbcXvP3T": []byte(`{"key":"thirdLens.identity.Lk2Pn6mQrtwzKbcXvP3T","actor":"vtx.identity.Lk2Pn6mQrtwzKbcXvP3T"}`),
	}
	siblingRevs := make(map[string]uint64, len(siblings))
	for k, v := range siblings {
		rev, perr := targetKV.Put(ctx, k, v)
		require.NoError(t, perr)
		siblingRevs[k] = rev
	}

	// The lens runs for real. An enrolled business lens is guarded (its
	// emptyBehavior is delete), and the §6.2 guard refuses a write carrying no
	// ordering token — so a sweep can only heal a lens that has actually
	// consumed, which is every live lens and none that never started. Running
	// the consumer is what makes this an end-to-end proof rather than a
	// reconstruction of one.
	p.RunOn(conn, e2eSpec(rule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipelineDone := make(chan struct{})
	go func() { defer close(pipelineDone); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-pipelineDone })

	identityID := stableNanoID("business-sweep-bob")
	identityKey := substrate.VertexKey("identity", identityID)
	provenanceAt := "2026-07-25T10:00:00Z"
	body := map[string]any{
		"key": identityKey, "class": "identity",
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"name": "business-sweep-bob"},
	}
	data, jerr := json.Marshal(body)
	require.NoError(t, jerr)
	_, err = coreKV.Put(ctx, identityKey, data)
	require.NoError(t, err)

	// The consumer projects it normally first — that is what gives the lens a
	// real ordering token and establishes the converged baseline.
	expectedKey := desc.BuildKey(identityKey)
	projectedDeadline := time.Now().Add(30 * time.Second)
	for {
		e, gerr := targetKV.Get(ctx, expectedKey)
		if gerr == nil && e != nil && len(e.Value) > 0 {
			break
		}
		if time.Now().After(projectedDeadline) {
			t.Fatal("the lens did not project the anchor within 30s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The divergence: the row is lost out of band — a restore, an errant purge,
	// the class of hole no CDC event will ever refill because the event that
	// would have is in the past. Nobody tells the sweep which actor went dark.
	require.NoError(t, targetKV.Purge(ctx, expectedKey))
	entry, err := targetKV.Get(ctx, expectedKey)
	require.True(t, err != nil || entry == nil || len(entry.Value) == 0,
		"the business row must be absent before the sweep runs")

	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	swept := make(chan struct{})
	go func() { defer close(swept); p.RunSweep(sweepCtx) }()

	// Healed: nobody named the actor. The sweep compared this lens's anchors
	// against the keys under its own prefix and found the hole itself.
	healDeadline := time.Now().Add(30 * time.Second)
	var healed *substrate.KVEntry
	for {
		e, gerr := targetKV.Get(ctx, expectedKey)
		if gerr == nil && e != nil && len(e.Value) > 0 {
			healed = e
			break
		}
		if time.Now().After(healDeadline) {
			t.Fatal("the business-lens convergence sweep did not heal the missing row within 30s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var env map[string]any
	require.NoError(t, json.Unmarshal(healed.Value, &env))
	require.Equal(t, expectedKey, env["key"])
	require.Equal(t, identityKey, env["actor"])
	// The heal count is folded in when the pass ends, which is strictly after
	// the write it counts — so it is polled, not read off the same instant the
	// row appeared.
	countDeadline := time.Now().Add(30 * time.Second)
	for sw.Status().Reconciled < 1 {
		if time.Now().After(countDeadline) {
			t.Fatal("a healed divergence must be counted so the heal is loud, not silent")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Converged: the streak clears and further ticks cost no writes.
	quietDeadline := time.Now().Add(30 * time.Second)
	for sw.Status().DivergentStreak != 0 {
		if time.Now().After(quietDeadline) {
			t.Fatalf("the sweep never reported a clean pass; streak=%d reconciled=%d",
				sw.Status().DivergentStreak, sw.Status().Reconciled)
		}
		time.Sleep(100 * time.Millisecond)
	}
	settled, err := targetKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	reconciledAtRest := sw.Status().Reconciled

	// Several more ticks against a converged world.
	time.Sleep(2 * time.Second)
	after, err := targetKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	require.Equal(t, settled.Revision, after.Revision, "a converged sweep must not rewrite the row")
	require.Equal(t, reconciledAtRest, sw.Status().Reconciled, "a converged sweep must not count phantom heals")

	// Siblings untouched — the property enrolment turned on. An unscoped listing
	// would hand this lens every other lens's keys, and the orphan direction
	// would retract them: their anchors are not among this lens's, so each reads
	// as a row whose anchor is gone.
	for k, wantRev := range siblingRevs {
		got, gerr := targetKV.Get(ctx, k)
		require.NoError(t, gerr, "sibling row %s must still exist", k)
		require.NotNil(t, got)
		require.Equal(t, wantRev, got.Revision,
			"sibling row %s must not be written by another lens's sweep", k)
		require.JSONEq(t, string(siblings[k]), string(got.Value),
			"sibling row %s must be byte-identical after a full sweep cycle", k)
	}

	// The cursor and heal count persist on the lens's own health entry, so a
	// restart resumes the walk rather than restarting it.
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
