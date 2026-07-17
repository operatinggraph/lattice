// Production-path proof for Story 14.2 — the §10.2 Option (b) keyColumn
// mechanism. A throwaway actor-aggregate PACKAGE lens declaring keyColumn
// projects through the LIVE Refractor pipeline (real InstallPackage →
// lens.CoreKVSource → projection.InstallActorAggregate, the exact path
// cmd/refractor calls) and emits a <targetId>.<bareNanoID> row key — the shape
// Weaver's splitRowKey accepts — instead of the default <type>.<id> suffix.
//
// The lens is type-agnostic: it uses a throwaway target prefix + a disjoint
// bucket (NOT weaver-targets, NOT leaseApplicationComplete) and a non-lease,
// non-service anchor (identity), so the mechanism cannot special-case any
// concrete type. The test asserts the bare-NanoID key on BOTH the project path
// AND the actor-disappearance delete path — the Q1 proof that BuildKey computes
// the same key on the call site that has a row and the one that has only the
// actorKey.
package refractor_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
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
	"github.com/asolgan/lattice/internal/testutil"
)

// proofConvergenceBucket is the throwaway lens's disjoint output bucket — NOT
// weaver-targets, so the mechanism is proven bucket-agnostic.
const proofConvergenceBucket = "proof-convergence"

// proofConvergenceTarget is the throwaway targetId prefix in the key pattern —
// NOT leaseApplicationComplete.
const proofConvergenceTarget = "proofConvergence"

// proofConvergenceSpec aggregates each identity's directly-assigned open tasks,
// anchored on the bound identity. A keyColumn lens's candidate IS its anchor, so
// one row per identity keyed on the identity's own bare id.
const proofConvergenceSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
RETURN
  identity.key AS actorKey,
  collect(DISTINCT { taskKey: task.key }) AS roster
`

func proofConvergencePackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "proof-convergence-pkg",
		Version:     "1.0.0",
		Description: "Throwaway actor-aggregate keyColumn lens for the 14.2 §10.2 Option (b) proof.",
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName:  "proofConvergence",
				Class:          "meta.lens",
				Adapter:        "nats-kv",
				Bucket:         proofConvergenceBucket,
				Engine:         "full",
				Spec:           proofConvergenceSpec,
				ProjectionKind: "actorAggregate",
				Output: &pkgmgr.OutputDescriptorSpec{
					AnchorType:       "identity",
					OutputKeyPattern: proofConvergenceTarget + ".{actorSuffix}",
					BodyColumns:      []string{"roster"},
					EmptyBehavior:    "delete",
					RealnessFilter:   "taskKey",
					Freshness:        "auto",
					KeyColumn:        "entityId",
				},
			},
		},
	}
}

func TestRefractor_KeyColumnLens_ProjectsBareNanoIDKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping keyColumn convergence proof e2e in -short mode")
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

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: proofConvergenceBucket})
	require.NoError(t, err)
	convKV, err := conn.OpenKV(ctx, proofConvergenceBucket)
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
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "proof-conv-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "proof-conv-meta",
		FilterSubjects: []string{"ops.meta"},
		AckWait:        5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	// --- install the throwaway keyColumn package via the REAL InstallPackage path ---
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	res, err := installer.Install(ctx, proofConvergencePackage())
	require.NoError(t, err, "InstallPackage of the throwaway keyColumn lens must succeed")
	require.NotNil(t, res)

	// --- live activation: CoreKVSource discovers the installed lens; the generic
	// actor-aggregate path wires it (no canonical-name, no type knowledge) ---
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

	var convRule *lens.Rule
	deadline := time.Now().Add(20 * time.Second)
	for convRule == nil {
		if time.Now().After(deadline) {
			t.Fatal("did not activate the proofConvergence lens within 20s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "proofConvergence" {
				convRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// The installed lens must carry the keyColumn the InstallPackage path wrote
	// onto the spec aspect and CoreKVSource parsed back — the three-mirror
	// round-trip. A missed mirror layer fails HERE, not in review.
	require.True(t, convRule.ProjectionKind == "actorAggregate",
		"installed lens must carry projectionKind=actorAggregate")
	require.NotNil(t, convRule.Output, "installed lens must carry the §6.13 Output descriptor")
	require.Equal(t, "entityId", convRule.Output.KeyColumn,
		"keyColumn must survive package-spec → InstallPackage → spec aspect → CoreKVSource round-trip")

	convAdpt, err := adapter.New(convKV, convRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(convRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, convAdpt, nil)
	require.NoError(t, err)
	require.NotNil(t, convRule.CompiledRule, "actor-aggregate lens must resolve a compiled rule")
	p.UseFullEngine(fullEngine, convRule.CompiledRule)
	require.True(t, projection.InstallActorAggregate(p, convAdpt, convRule, projectionRevision, adjKV, coreKV, logger),
		"proofConvergence lens must install through projection.InstallActorAggregate")

	p.RunOn(conn, e2eSpec(convRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-doneCh })

	// --- fixture: identity + one open task assigned to it ---
	identityID := stableNanoID("keycolumn-conv-identity")
	taskID := stableNanoID("keycolumn-conv-task")
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

	// The §10.2 Option (b) key: <targetId>.<bareNanoID> — the identity's OWN bare
	// id, one dot after the targetId. NOT <targetId>.identity.<id>.
	bareKey := proofConvergenceTarget + "." + identityID
	defaultKey := proofConvergenceTarget + ".identity." + identityID

	// --- PROJECT: the open task projects a convergence row keyed by the bare id ---
	require.Eventually(t, func() bool {
		entry, gErr := convKV.Get(ctx, bareKey)
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
	}, 30*time.Second, 100*time.Millisecond, "the keyColumn lens did not project at the bare-NanoID key")

	// The default <targetId>.<type>.<id> key must NOT exist — proves the suffix is
	// the bare id, not the default <type>.<id>.
	defEntry, defErr := convKV.Get(ctx, defaultKey)
	require.True(t, defErr != nil || defEntry == nil || len(defEntry.Value) == 0,
		"the default <type>.<id> key must NOT be written when keyColumn is set: %q", defaultKey)

	// The projected key's tail (after the first dot) must satisfy the exact
	// predicate Weaver's splitRowKey applies (internal/weaver/evaluator.go:514),
	// proving the round-trip — see TestSplitRowKey_AcceptsKeyColumnProjectedKey.
	tail := bareKey[strings.IndexByte(bareKey, '.')+1:]
	require.True(t, substrate.IsValidNanoID(tail),
		"projected key tail %q must be a bare NanoID (splitRowKey predicate)", tail)
	require.Equal(t, 1, strings.Count(bareKey, "."),
		"keyColumn key must have exactly one dot after the targetId: %q", bareKey)

	// --- DELETE PATH (the Q1 proof): closing the task reprojects to zero rows →
	// emptyBehavior:delete drives the delete. BuildKey is wired on the delete path
	// with ONLY the actorKey (no row); if the key were row-sourced it would target
	// a different key and the bare-NanoID row would never retract. Assert the
	// delete lands on the SAME bareKey. ---
	writeVertex(taskKey, "task", map[string]any{"status": "complete"})
	require.Eventually(t, func() bool {
		entry, gErr := convKV.Get(ctx, bareKey)
		if gErr != nil {
			// hard delete on the unguarded adapter removes the key — acceptable.
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
	}, 30*time.Second, 100*time.Millisecond, "closing the task did not retract the convergence row at the bare-NanoID key")
}
