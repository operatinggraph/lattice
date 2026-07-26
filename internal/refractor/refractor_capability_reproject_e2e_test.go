// Auth-plane reconciliation e2e: the per-actor `reproject` verb heals a
// capability document that a lost CDC event never projected
// (capability-projection-reconciliation-design.md §3.1).
//
// The incident this reproduces (design §2.2/§2.3): a grant lands in Core KV
// while the lens pipeline is not consuming — a restart window, or a
// multi-ten-minute drain after a source stream is recreated. The pipeline
// never sees that event, nothing re-drives it, and the actor's cap document
// stays physically absent forever, so every step-3 authorization for it
// answers NoCapabilityEntry. Here the gap is made exact by never starting the
// pipeline's consumer: the graph is written, no CDC is consumed, and the key
// is asserted absent before the verb is invoked.
//
// The reconciler is lens-agnostic — it is driven by the envelope wrapper plus
// reprojectActors, not by any canonical name — so the primordial capability
// lens exercises the same path rbac-domain's capabilityRoles takes in
// production, without this test having to install a package.
package refractor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func TestRefractor_Reproject_HealsLostProjection_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reproject e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test-reproject", logger)
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
	require.NotNil(t, capabilityRule.CompiledRule)

	targetKV, err := conn.OpenKV(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	adpt, err := adapter.New(targetKV, capabilityRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	p, err := pipeline.New(capabilityRule.ID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
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

	// The availability gap: the pipeline is constructed and can evaluate, but
	// its consumer is deliberately never started, so no CDC event for the
	// fixture below is ever applied. Reproject reads live Core KV, not the
	// stream — that is precisely why it can heal a gap the pipeline missed.

	identityID := stableNanoID("reproject-alice")
	identityKey := substrate.VertexKey("identity", identityID)
	provenanceAt := "2026-07-21T10:00:00Z"

	writeVertex := func(key, class string, extra map[string]any) {
		body := map[string]any{
			"key":            key,
			"class":          class,
			"createdAt":      provenanceAt,
			"lastModifiedAt": provenanceAt,
			"data":           extra,
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, data)
		require.NoError(t, perr)
	}
	writeVertex(identityKey, "identity", map[string]any{"name": "reproject-alice", "protected": true})
	holdsRoleLinkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", bootstrap.RoleOperatorID)
	linkBody, lerr := bootstrap.MakeLinkEnvelope(holdsRoleLinkKey, identityKey, bootstrap.RoleOperatorKey, "holdsRole", "holdsRole", nil)
	require.NoError(t, lerr)
	_, lerr = coreKV.Put(ctx, holdsRoleLinkKey, linkBody)
	require.NoError(t, lerr)

	// The reprojection cypher walks adjacency, which the running bootstrapper
	// builds from its own Core KV consumer — a consumer independent of the
	// lens pipeline, which is why the graph can be complete while the
	// capability document is missing. Wait for the edge so the assertion
	// below tests reconciliation, not a race with adjacency.
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

	expectedKey := "cap.identity." + identityID

	// Absence is the defect: nothing consumed the grant, so step 3 would
	// answer NoCapabilityEntry for this actor indefinitely.
	entry, err := capabilityKV.Get(ctx, expectedKey)
	require.True(t, err != nil || entry == nil || len(entry.Value) == 0,
		"capability doc must be absent before reconciliation (the lost-event state)")

	// The targeted heal.
	res, err := p.Reproject(ctx, identityKey)
	require.NoError(t, err)
	require.True(t, res.Wrote, "a divergent actor must be healed by a write")
	require.False(t, res.Converged)
	require.False(t, res.Deleted)

	entry, err = capabilityKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	require.NotNil(t, entry)
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	require.Equal(t, expectedKey, env["key"])
	require.Equal(t, identityKey, env["actor"])
	require.Equal(t, "1.0", env["version"])

	// Idempotence: the actor is now converged, so a second pass must write
	// nothing at all — the property that makes Fire 1b's sweep free at rest.
	before := entry.Revision
	res2, err := p.Reproject(ctx, identityKey)
	require.NoError(t, err)
	require.True(t, res2.Converged, "a converged actor must not be rewritten")
	require.False(t, res2.Wrote)

	after, err := capabilityKV.Get(ctx, expectedKey)
	require.NoError(t, err)
	require.Equal(t, before, after.Revision,
		"a converged reconciliation must not bump the stored revision")
}
