// Package refractor_test — e2e for varlength-anchor-derivation-design.md §14
// T12: a `containedIn` link create on one actor's workplace chain must be
// answered by the anchor derivation, bounded to that one actor, for
// packages/edge-manifest's generated edgeManifestStaffReadGrants producer.
//
// edgeManifestStaffReadGrants is the domain producer whose staged WITH
// boundaries re-open their own shared chains identically at every stage —
// `role` off `chainHeldRoles`, `work`/`place` off the worksAt+containedIn
// spine — which is exactly the shape judgeMatch admits, so it carries a
// complete anchor hop index and reaches the derivation's `act` path. Its
// sibling base-domain producer edgeManifestReadGrants binds `op` over two
// genuinely different chains across a staging boundary and stays refused
// (anchor_hopindex_corpus_census_test.go pins both verdicts); this test
// targets the producer the derivation actually acts on.
//
// Activated through the real generated-producer install path (mirroring
// edge_manifest_fire2_producer_flip_e2e_test.go), the test pins three
// things: the event is answered by the derivation rather than the
// ActorEnumerator BFS; the reprojection touches only the affected actor, not
// every actor in the bucket; and a second, independently-observed event
// proves the derived set does not fall short of what the BFS itself would
// reach for it.
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

func TestEdgeManifestStaff_AnchorDerivation_ContainedInCreate_ReprojectsOnlyTheAffectedActor_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping edge-manifest staff anchor-derivation e2e in -short mode")
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

	// --- install the real edge-manifest package, exactly as
	// edge_manifest_fire2_producer_flip_e2e_test.go does. ---
	metaCP, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "em-staffderiv-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName: "core-operations", Durable: "em-staffderiv-meta",
		FilterSubjects: []string{"ops.meta"}, AckWait: 5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { metaCP.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	installer := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = testutil.StandardRoleIDs()
	_, err = installer.Install(ctx, edgemanifest.Package)
	require.NoError(t, err, "installing edge-manifest must succeed")

	// --- activate ONLY the generated edgeManifestStaffReadGrants producer,
	// through the exact production path (CoreKVSource discovery +
	// projection.InstallActorAggregate). ---
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
			t.Fatal("did not activate the edgeManifestStaffReadGrants producer lens within deadline")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "edgeManifestStaffReadGrants" {
				producerRule = r
			}
		case <-time.After(remaining):
		}
	}

	require.True(t, projection.IsActorAggregate(producerRule), "edgeManifestStaffReadGrants must carry projectionKind=actorAggregate")
	require.NotNil(t, producerRule.Output, "edgeManifestStaffReadGrants must carry the §6.13 Output descriptor")
	require.Equal(t, "anchorId", producerRule.Output.EntryKeyColumn)
	require.True(t, projection.IsAuthPlane(producerRule), "edgeManifestStaffReadGrants targets capability-kv and must be classified auth-plane")

	fullCR, isFull := producerRule.CompiledRule.(*full.CompiledRule)
	require.True(t, isFull, "the generated producer must compile to the full engine")
	ix := fullCR.AnchorHopIndex()
	require.Truef(t, ix.Complete,
		"edgeManifestStaffReadGrants must index completely — %q means the WITH-scope re-binding this test exists to "+
			"exercise never landed, and the derivation cannot act on this lens at all", ix.Incomplete)

	adpt, err := adapter.New(capKV, producerRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(producerRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	require.NotNil(t, producerRule.CompiledRule)
	p.UseFullEngine(fullEngine, producerRule.CompiledRule)
	require.True(t, projection.InstallActorAggregate(p, adpt, producerRule, projectionRevision, adjKV, coreKV, logger),
		"edgeManifestStaffReadGrants must install through projection.InstallActorAggregate")

	p.RunOn(conn, e2eSpec(producerRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipelineDone := make(chan struct{})
	go func() { defer close(pipelineDone); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-pipelineDone })

	// --- fixture topology: two unrelated identities, each worksAt their own
	// building with a work order already reachable at the building's own
	// level (0-hop `containedIn*0..`), plus a unit under actor A's building
	// that is NOT YET linked in, and a work order sitting at that unit. The
	// unit's work order is unreachable from A until the containedIn link
	// below lands — that link create is the event under test. B is untouched
	// by it, and exists solely to prove the reprojection does not fan out to
	// it. ---
	techAID := pl2NanoID("staffderiv-techA")
	techBID := pl2NanoID("staffderiv-techB")
	bldgAID := pl2NanoID("staffderiv-bldgA")
	bldgBID := pl2NanoID("staffderiv-bldgB")
	unitA1ID := pl2NanoID("staffderiv-unitA1")
	woBldgAID := pl2NanoID("staffderiv-woBldgA")
	woBldgBID := pl2NanoID("staffderiv-woBldgB")
	woUnitID := pl2NanoID("staffderiv-woUnit")

	techAKey := substrate.VertexKey("identity", techAID)
	techBKey := substrate.VertexKey("identity", techBID)

	emWriteVertex(t, ctx, coreKV, techAKey, "identity", map[string]any{"name": "Staff Deriv Tech A"})
	emWriteVertex(t, ctx, coreKV, techBKey, "identity", map[string]any{"name": "Staff Deriv Tech B"})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("building", bldgAID), "building", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("building", bldgBID), "building", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("unit", unitA1ID), "unit", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("workorder", woBldgAID), "workorder", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("workorder", woBldgBID), "workorder", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("workorder", woUnitID), "workorder", map[string]any{})

	emWriteLink(t, ctx, coreKV, "identity", techAID, "worksAt", "building", bldgAID)
	emWriteLink(t, ctx, coreKV, "identity", techBID, "worksAt", "building", bldgBID)
	emWriteLink(t, ctx, coreKV, "workorder", woBldgAID, "locatedAt", "building", bldgAID)
	emWriteLink(t, ctx, coreKV, "workorder", woBldgBID, "locatedAt", "building", bldgBID)
	// unitA1 is a real vertex but carries no containedIn link yet, and
	// woUnit is locatedAt it — so woUnit is reachable from nobody until the
	// event below.
	emWriteLink(t, ctx, coreKV, "workorder", woUnitID, "locatedAt", "unit", unitA1ID)

	perAnchorKey := func(actorID, anchorID string) string {
		return "cap-read.edgeManifestStaff.identity." + actorID + "." + anchorID
	}

	readEntry := func(key string) (isDeleted, present bool) {
		entry, gErr := capKV.Get(ctx, key)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false, false
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &env), "value at %q must be valid JSON, got %q", key, entry.Value)
		del, _ := env["isDeleted"].(bool)
		return del, true
	}

	// --- baseline admits: each tech's own building-level work order, and
	// nothing else — proving the unit's work order really starts
	// unreachable rather than merely unobserved. ---
	require.Eventually(t, func() bool {
		del, present := readEntry(perAnchorKey(techAID, woBldgAID))
		return present && !del
	}, 30*time.Second, 100*time.Millisecond, "tech A's own building-level work order must be granted")
	require.Eventually(t, func() bool {
		del, present := readEntry(perAnchorKey(techBID, woBldgBID))
		return present && !del
	}, 10*time.Second, 100*time.Millisecond, "tech B's own building-level work order must be granted")
	_, present := readEntry(perAnchorKey(techAID, woUnitID))
	require.False(t, present, "the unit's work order must not be reachable before the containedIn link exists")

	techBEntry, err := capKV.Get(ctx, perAnchorKey(techBID, woBldgBID))
	require.NoError(t, err)
	techBRevisionBefore := techBEntry.Revision

	before := p.AnchorDerivationShadow()

	// --- THE EVENT: a containedIn link create that folds the unit into
	// actor A's workplace chain. ---
	emWriteLink(t, ctx, coreKV, "unit", unitA1ID, "containedIn", "building", bldgAID)

	require.Eventually(t, func() bool {
		del, present := readEntry(perAnchorKey(techAID, woUnitID))
		return present && !del
	}, 30*time.Second, 100*time.Millisecond,
		"the containedIn link must fold the unit's work order into actor A's grants")

	after := p.AnchorDerivationShadow()

	// (1) The event was answered by the derivation, not the enumerator.
	require.Greater(t, after.Acted, before.Acted,
		"a containedIn create on the worksAt+containedIn spine must be answered by the anchor derivation — "+
			"if this never moves, the WITH-scope re-binding admitted for this producer never reached the act path "+
			"and the event fell back to the BFS instead")
	require.Equal(t, before.FellBack, after.FellBack,
		"this event must not have fallen back — a complete index exists for the whole staged producer")

	// (2) The reprojection is bounded to the affected actor, not every actor
	// the bucket holds: tech B's own row must not have moved at all.
	techBEntryAfter, err := capKV.Get(ctx, perAnchorKey(techBID, woBldgBID))
	require.NoError(t, err)
	require.Equal(t, techBRevisionBefore, techBEntryAfter.Revision,
		"tech B shares no chain with the event and must not be reprojected — "+
			"a revision bump here would mean the derivation (or its fallback) re-executed the whole bucket")

	// (3) Soundness: the derived set must not fall short of what the trusted
	// enumerator BFS itself would reach. Phase (1)/(2) proved the ACT path
	// converged the row correctly, which already rules out an under-count on
	// THIS event; this phase asks the same question of a second, structurally
	// identical event and gets it answered by the shadow comparison the
	// derivation ships for exactly this purpose (anchor_derivation_shadow.go)
	// — sampled at 1-in-1 so the one event below is guaranteed to be measured,
	// and observed the same way TestObjectAttachments_DerivationActsOnANeighbourEvent_E2E
	// observes the act-mode tally: a before/after delta across one event.
	p.SetAnchorDerivationSampling(1)
	p.SetAnchorDerivationMode(pipeline.DerivationModeShadow)

	techCID := pl2NanoID("staffderiv-techC")
	bldgCID := pl2NanoID("staffderiv-bldgC")
	unitC1ID := pl2NanoID("staffderiv-unitC1")
	woUnitCID := pl2NanoID("staffderiv-woUnitC")

	techCKey := substrate.VertexKey("identity", techCID)
	emWriteVertex(t, ctx, coreKV, techCKey, "identity", map[string]any{"name": "Staff Deriv Tech C"})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("building", bldgCID), "building", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("unit", unitC1ID), "unit", map[string]any{})
	emWriteVertex(t, ctx, coreKV, substrate.VertexKey("workorder", woUnitCID), "workorder", map[string]any{})
	emWriteLink(t, ctx, coreKV, "identity", techCID, "worksAt", "building", bldgCID)
	emWriteLink(t, ctx, coreKV, "workorder", woUnitCID, "locatedAt", "unit", unitC1ID)

	shadowBefore := p.AnchorDerivationShadow()

	emWriteLink(t, ctx, coreKV, "unit", unitC1ID, "containedIn", "building", bldgCID)

	require.Eventually(t, func() bool {
		del, present := readEntry(perAnchorKey(techCID, woUnitCID))
		return present && !del
	}, 30*time.Second, 100*time.Millisecond,
		"shadow mode still reprojects off the enumerator's own answer, so this must converge exactly as phase (1) did")

	shadowAfter := p.AnchorDerivationShadow()
	require.Greaterf(t, shadowAfter.Sampled, shadowBefore.Sampled,
		"the sampling was forced to 1-in-1; the shadow comparison must have run at least once for this event")
	require.Equal(t, shadowBefore.DivergentEvents, shadowAfter.DivergentEvents,
		"the derived set must never name an anchor the enumerator BFS does not reach")
	require.Greaterf(t, shadowAfter.Agreed, shadowBefore.Agreed,
		"the derived set must equal the enumerator's answer for this event — a narrowed or divergent verdict here "+
			"is the one soundness direction that matters: a smaller derived set than the BFS's is a grant or "+
			"revocation this producer would silently never reproject")
}
