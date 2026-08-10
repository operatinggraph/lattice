// Package refractor_test — end-to-end proof for
// cap-read-per-anchor-grant-keys-design.md Fire 2 (producer flips):
// packages/edge-manifest's REAL generated cap-read producer
// (edgeManifestReadGrants, compiled by pkgmgr.generateProducerLens) now emits
// `entryKeyColumn` — the per-anchor keyed shape Fire 1 built the mechanism
// for — rather than the legacy one-document-per-actor shape. This proves the
// flip through the live pipeline, not a unit-level driver call: admit on
// grant, drop on revoke, and drain of a pre-existing legacy parent document on
// the actor's first post-flip evaluation (§6 dual-read migration).
//
// Installs the real edgemanifest.Package via InstallPackage (mirroring
// edge_manifest_fire1_e2e_test.go), then activates ONLY the generated
// edgeManifestReadGrants producer (nats-kv, actorAggregate, capability-kv
// bucket) through the same projection.InstallActorAggregate path
// cmd/refractor's startPipeline uses for every actor-aggregate lens
// (refractor_package_actoraggregate_proof_e2e_test.go's pattern) — which is
// what auto-installs the §6.2 guard, the perEntry envelope, and the prefix-
// diff retraction for a capability-kv-bucket lens, with no test-side guard
// wiring. The fixture exercises edgeInstances' single-hop `providedTo` walk
// (one of domainBase's nine member walks) — the other walks' OPTIONAL MATCH
// branches simply contribute no anchors for an actor with no matching
// topology, so seeding one walk's fixture is sufficient to prove the
// generic per-domain producer.
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
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

func TestEdgeManifest_Fire2_E2E_ProducerFlip_AdmitRevokeLegacyDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping edge-manifest Fire 2 producer-flip e2e in -short mode")
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
	js := conn.JetStream()

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- install the real edge-manifest package via the REAL InstallPackage
	// path (meta-lane, stub-auth) — same mechanics as edge_manifest_fire1_
	// e2e_test.go's install of service-domain. ---
	metaCP, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "em-fire2-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName: "core-operations", Durable: "em-fire2-meta",
		FilterSubjects: []string{"ops.meta"}, AckWait: 5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { metaCP.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	installer := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	// Every identity-domain role, derived from the package itself rather than
	// listed by hand: edge-manifest offers panes to roles this fixture does
	// not otherwise care about, and a hand-listed subset fails the install the
	// moment a pane names one more.
	installer.RoleIDs = testutil.StandardRoleIDs()
	_, err = installer.Install(ctx, edgemanifest.Package)
	require.NoError(t, err, "installing edge-manifest must succeed")

	// --- activate ONLY the generated edgeManifestReadGrants producer (nats-kv,
	// actorAggregate) through the exact production path cmd/refractor's
	// startPipeline uses: CoreKVSource discovers the installed lens,
	// projection.InstallActorAggregate wires the guard + perEntry envelope +
	// prefix-diff retraction generically off the lens's own §6.13 descriptor
	// (capability-kv bucket ⇒ projection.IsAuthPlane ⇒ guarded). ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test", logger)
	loaded := make(chan *lens.Rule, 32)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case loaded <- r:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var producerRule *lens.Rule
	deadline := time.Now().Add(20 * time.Second)
	for producerRule == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("did not activate the edgeManifestReadGrants producer lens within deadline")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "edgeManifestReadGrants" {
				producerRule = r
			}
		case <-time.After(remaining):
		}
	}

	require.True(t, projection.IsActorAggregate(producerRule), "edgeManifestReadGrants must carry projectionKind=actorAggregate")
	require.NotNil(t, producerRule.Output, "edgeManifestReadGrants must carry the §6.13 Output descriptor")
	require.Equal(t, "anchorId", producerRule.Output.EntryKeyColumn,
		"Fire 2: the generated producer must set entryKeyColumn — this is the flip under test")
	require.True(t, projection.IsAuthPlane(producerRule), "edgeManifestReadGrants targets capability-kv and must be classified auth-plane")

	adpt, err := adapter.New(capKV, producerRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(producerRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	require.NotNil(t, producerRule.CompiledRule)
	p.UseFullEngine(fullEngine, producerRule.CompiledRule)
	require.True(t, projection.InstallActorAggregate(p, adpt, producerRule, projectionRevision, adjKV, coreKV, logger),
		"edgeManifestReadGrants must install through projection.InstallActorAggregate")

	p.RunOn(conn, e2eSpec(producerRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipelineDone := make(chan struct{})
	go func() { defer close(pipelineDone); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-pipelineDone })

	// --- seed a LEGACY 4-token doc for the actor BEFORE any evaluation reaches
	// it (§6 dual-read migration transport: the first post-flip evaluation
	// must guard-tombstone this parent doc in the same batch as the fresh
	// per-anchor upserts). ---
	tenantID := pl2NanoID("em-fire2-tenant")
	legacyKey := "cap-read.edgeManifest.identity." + tenantID
	legacyBody, err := json.Marshal(map[string]any{
		"isDeleted": false, "readableAnchors": []any{},
		"version": "1.0", "projectedAt": "2026-07-28T00:00:00Z", "projectionSeq": 3,
	})
	require.NoError(t, err)
	_, err = capKV.Put(ctx, legacyKey, legacyBody)
	require.NoError(t, err)

	// --- fixture topology: a tenant identity with one service instance
	// providedTo it — edgeInstances' walk (domainBase), the simplest
	// single-hop member of the generated producer's nine branches. ---
	instID := pl2NanoID("em-fire2-instance")
	instKey := substrate.VertexKey("service", instID)
	identityKey := substrate.VertexKey("identity", tenantID)

	emWriteVertex(t, ctx, coreKV, instKey, "service.backgroundCheck.instance", map[string]any{})
	emWriteVertex(t, ctx, coreKV, identityKey, "identity", map[string]any{"name": "Fire2 Tenant"})
	emWriteLink(t, ctx, coreKV, "service", instID, "providedTo", "identity", tenantID)

	perAnchorKey := "cap-read.edgeManifest.identity." + tenantID + "." + instID

	readTombstone := func(key string) (isDeleted, present bool) {
		entry, gErr := capKV.Get(ctx, key)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false, false
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &env), "value at %q must be valid JSON, got %q", key, entry.Value)
		del, _ := env["isDeleted"].(bool)
		return del, true
	}

	// --- ADMIT: the per-anchor key appears, live. ---
	require.Eventually(t, func() bool {
		del, present := readTombstone(perAnchorKey)
		return present && !del
	}, 30*time.Second, 100*time.Millisecond,
		"the per-anchor grant key must appear once the providedTo link lands")

	// --- LEGACY DRAIN: the pre-existing legacy parent doc must be
	// guard-tombstoned in the same evaluation that wrote the fresh key. ---
	require.Eventually(t, func() bool {
		del, present := readTombstone(legacyKey)
		return present && del
	}, 10*time.Second, 100*time.Millisecond,
		"the legacy parent doc must be tombstoned on the actor's first post-flip evaluation (§6 dual-read drain)")

	// --- REVOKE: tombstone the providedTo link; the per-anchor key must drop. ---
	linkKey := substrate.LinkKey("service", instID, "providedTo", "identity", tenantID)
	tombstone := map[string]any{
		"key": linkKey, "class": "providedTo", "isDeleted": true,
		"sourceVertex": instKey, "targetVertex": identityKey, "localName": "providedTo",
	}
	tombstoneBody, err := json.Marshal(tombstone)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, tombstoneBody)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		del, present := readTombstone(perAnchorKey)
		return !present || del
	}, 20*time.Second, 100*time.Millisecond,
		"revoking the providedTo link must tombstone the per-anchor grant key")
}
