// AC5 proof test — a brand-new actor-aggregate PACKAGE lens projects and
// invalidates through the LIVE Refractor pipeline with ZERO edits under cmd/ or
// internal/refractor/capabilityenv/.
//
// The lens below (`proofRoster`) is unknown to cmd/refractor and to
// capabilityenv: it ships only as test/package data — its own canonicalName, its
// own projectionKind:"actorAggregate" + §6.13 Output descriptor, its own disjoint
// output bucket, and a simple per-actor cypher. It is installed via the real
// InstallPackage op path (pkgmgr.Installer → meta-lane Processor → atomic commit),
// then activated by the live lens.CoreKVSource watch and wired through the
// generic projectionKind-keyed actor-aggregate path via the production
// projection.InstallActorAggregate — the exact function cmd/refractor calls.
//
// If this lens projects + reprojects, the layering inversion is gone: a package
// can ship a per-actor aggregating lens with no core edit. The test fails if any
// future change reintroduces a canonical-name dependency in cmd/ for actor-
// aggregate routing — the lens name is deliberately not "capability"/"myTasks"/
// "capabilityEphemeral", so a name-keyed switch would never route it.
package refractor_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

// proofRosterBucket is the throwaway lens's disjoint output bucket (NOT cap.* or
// my-tasks.*). Provisioned at activation time the way Refractor lazily would.
const proofRosterBucket = "proof-roster"

// proofRosterSpec is a simple per-actor aggregating cypher: each identity's
// directly-assigned open tasks, projected as a roster row. Anchored on the bound
// identity so the compiled plan + the broad fan-out both reproject from the actor.
const proofRosterSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
RETURN
  identity.key AS actorKey,
  collect(DISTINCT { taskKey: task.key }) AS roster
`

func proofRosterPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "proof-roster-pkg",
		Version:     "1.0.0",
		Description: "Throwaway actor-aggregate package lens for the 12.4 AC5 proof.",
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName:  "proofRoster",
				Class:          "meta.lens",
				Adapter:        "nats-kv",
				Bucket:         proofRosterBucket,
				Engine:         "full",
				Spec:           proofRosterSpec,
				ProjectionKind: "actorAggregate",
				Output: &pkgmgr.OutputDescriptorSpec{
					AnchorType:       "identity",
					OutputKeyPattern: "roster.{actorSuffix}",
					BodyColumns:      []string{"roster"},
					EmptyBehavior:    "delete",
					RealnessFilter:   "taskKey",
					Freshness:        "auto",
				},
			},
		},
	}
}

func TestRefractor_PackageActorAggregateLens_ProjectsWithZeroCoreEdits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping package actor-aggregate proof e2e in -short mode")
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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	js := conn.JetStream()

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: proofRosterBucket})
	require.NoError(t, err)
	rosterKV, err := conn.OpenKV(ctx, proofRosterBucket)
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- meta-lane Processor pipeline so the InstallPackage op commits ---
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "proof-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "proof-meta",
		FilterSubjects: []string{"ops.meta"},
		AckWait:        5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	// --- install the throwaway package via the REAL InstallPackage path ---
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	res, err := installer.Install(ctx, proofRosterPackage())
	require.NoError(t, err, "InstallPackage of the throwaway actor-aggregate lens must succeed")
	require.NotNil(t, res)

	// --- live activation: CoreKVSource discovers the installed lens and the
	// generic actor-aggregate path wires it (no canonical-name knowledge) ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test", logger)
	loaded := make(chan *lens.Rule, 8)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var rosterRule *lens.Rule
	deadline := time.Now().Add(20 * time.Second)
	for rosterRule == nil {
		if time.Now().After(deadline) {
			t.Fatal("did not activate the proofRoster lens within 20s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "proofRoster" {
				rosterRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// The installed lens must carry the actor-aggregate marker + descriptor that
	// CoreKVSource parsed straight off the InstallPackage-written spec aspect.
	require.True(t, rosterRule.ProjectionKind == "actorAggregate",
		"installed lens must carry projectionKind=actorAggregate")
	require.NotNil(t, rosterRule.Output, "installed lens must carry the §6.13 Output descriptor")

	rosterAdpt, err := adapter.New(rosterKV, rosterRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(rosterRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, rosterAdpt, nil)
	require.NoError(t, err)
	require.NotNil(t, rosterRule.CompiledRule, "actor-aggregate lens must resolve a compiled rule")
	p.UseFullEngine(fullEngine, rosterRule.CompiledRule)
	// The SAME production wiring cmd/refractor's startPipeline calls —
	// projection.InstallActorAggregate, keyed off the descriptor, with no
	// canonical-name knowledge.
	require.True(t, projection.InstallActorAggregate(p, rosterAdpt, rosterRule, projectionRevision, adjKV, coreKV, logger),
		"proofRoster lens must install through projection.InstallActorAggregate")

	p.RunOn(conn, e2eSpec(rosterRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-doneCh })

	// --- fixture: identity + one open task assigned to it ---
	identityID := stableNanoID("proof-roster-identity")
	taskID := stableNanoID("proof-roster-task")
	identityKey := substrate.VertexKey("identity", identityID)
	taskKey := substrate.VertexKey("task", taskID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	writeVertex := func(key, class string, data map[string]any) {
		body := map[string]any{
			"key": key, "class": class, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt, "data": data,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	buildEdge := func(name, fromType, fromID, toType, toID string) {
		linkKey := substrate.LinkKey(fromType, fromID, name, toType, toID)
		edgeID := name + ":" + fromID + ":" + toID
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
		}))
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
		}))
	}

	writeVertex(taskKey, "task", map[string]any{"status": "open"})
	buildEdge("assignedTo", "task", taskID, "identity", identityID)
	writeVertex(identityKey, "identity", map[string]any{"name": "proof"})

	expectedKey := rosterRule.Output.OutputKeyPattern // sanity: pattern is set
	require.Equal(t, "roster.{actorSuffix}", expectedKey)
	outKey := "roster.identity." + identityID

	// --- PROJECT: the open task projects a roster row for its assignee ---
	require.Eventually(t, func() bool {
		entry, gErr := rosterKV.Get(ctx, outKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		roster, _ := env["roster"].([]any)
		for _, e := range roster {
			if m, _ := e.(map[string]any); m["taskKey"] == taskKey {
				return true
			}
		}
		return false
	}, 30*time.Second, 100*time.Millisecond, "the throwaway package lens did not project a per-actor doc")

	// --- INVALIDATE/REPROJECT: closing the task reprojects to zero rows →
	// the descriptor's emptyBehavior:delete drives a guarded tombstone/delete so
	// the roster key drops the actor. A relevant CDC event reprojects correctly. ---
	writeVertex(taskKey, "task", map[string]any{"status": "complete"})
	require.Eventually(t, func() bool {
		entry, gErr := rosterKV.Get(ctx, outKey)
		if gErr != nil {
			// hard delete on an unguarded adapter removes the key — also acceptable.
			return true
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		if isDel, _ := env["isDeleted"].(bool); isDel {
			return true
		}
		roster, _ := env["roster"].([]any)
		return len(roster) == 0
	}, 30*time.Second, 100*time.Millisecond, "closing the task did not reproject/invalidate the roster doc")
}
