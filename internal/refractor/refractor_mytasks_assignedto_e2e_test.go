// my-tasks lens REALISTIC-ordering e2e: reproduces the real task-assignment
// lifecycle, where the assignee identity exists LONG BEFORE any task is assigned
// to it. The links are created as genuine Contract #1 link envelopes written to
// Core KV (producing CDC) — NOT pre-seeded directly into adjacency — so the
// reprojection trigger for the inbound `assignedTo` link is exercised end-to-end
// through the real pipeline + the dedicated adjacency consumer.
//
// This is the regression guard for the bug where a freshly-assigned task never
// projected into `my-tasks` because the identity-anchored lens did not reproject
// on the inbound `task -[:assignedTo]-> identity` link mutation.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

func TestRefractor_MyTasksLens_AssignedToTrigger_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping my-tasks assignedTo-trigger e2e test in -short mode")
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

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: orchestrationbase.MyTasksBucket})
	require.NoError(t, err)
	myTasksKV, err := conn.OpenKV(ctx, orchestrationbase.MyTasksBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	fullEngine := full.New()
	var myTasksLensSpec pkgmgr.LensSpec
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "myTasks" {
			myTasksLensSpec = l
		}
	}
	require.NotEmpty(t, myTasksLensSpec.Spec, "myTasks lens spec must exist")
	cr, err := fullEngine.Parse(myTasksLensSpec.Spec)
	require.NoError(t, err, "myTasks spec must parse")

	adpt, err := adapter.New(myTasksKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	adpt.SetGuarded(true)

	const lensID = "MyTasksAsgnLensId001"
	p, err := pipeline.New(lensID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(fullEngine, cr)
	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	myTasksDesc := descFromPkgSpec(t, myTasksLensSpec)
	p.SetEnvelopeFn(myTasksDesc.EnvelopeFn("vtx.meta."+lensID, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, myTasksDesc.AnchorType))
	p.SetActorDeleteKey(myTasksDesc.BuildKey)

	p.RunOn(conn, e2eSpec(lensID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-doneCh })

	identityID := stableNanoID("mytasks-asgn-assignee")
	taskID := stableNanoID("mytasks-asgn-task")
	opID := stableNanoID("mytasks-asgn-op")
	targetID := stableNanoID("mytasks-asgn-target")

	identityKey := substrate.VertexKey("identity", identityID)
	taskKey := substrate.VertexKey("task", taskID)
	opKey := substrate.VertexKey("meta", opID)
	targetKey := substrate.VertexKey("leaseapp", targetID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	writeVertex := func(key, class string, extra map[string]any) {
		body := map[string]any{
			"key": key, "class": class, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
			"data": extra,
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, data)
		require.NoError(t, perr)
	}
	// writeLink writes a genuine Contract #1 link envelope to Core KV (key shape
	// lnk.<srcType>.<srcId>.<rel>.<dstType>.<dstId>). The Put produces CDC that
	// the dedicated adjacency consumer turns into adjacency edges AND that the
	// actor-aware myTasks pipeline must reproject on. This is the REAL flow — no
	// direct adjacency seeding.
	writeLink := func(srcType, srcID, rel, dstType, dstID string) {
		linkKey := substrate.LinkKey(srcType, srcID, rel, dstType, dstID)
		body := map[string]any{
			"key":          linkKey,
			"class":        "link",
			"isDeleted":    false,
			"createdAt":    provenanceAt,
			"lastModifiedAt": provenanceAt,
			"sourceVertex": substrate.VertexKey(srcType, srcID),
			"targetVertex": substrate.VertexKey(dstType, dstID),
			"localName":    rel,
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, linkKey, data)
		require.NoError(t, perr)
	}

	// --- REALISTIC ORDER: the identity exists FIRST, long before any task. ---
	writeVertex(identityKey, "identity", map[string]any{"name": "applicant"})

	// Give the identity-anchor CDC time to project (it yields a deleted/absent row:
	// the identity has no open task yet — the correct empty state).
	expectedKey := myTasksDesc.BuildKey(identityKey)
	time.Sleep(2 * time.Second)

	// --- LATER: a task is created and assigned to the long-existing identity. ---
	// The op meta-vertex is the operation's DDL, exactly as a dispatched userTask's
	// forOperation points at it: the human name lives on the ROOT as
	// data.operationType (NOT a .canonicalName aspect — package op DDLs carry none),
	// and there is no .description aspect (the optional authoring nicety is absent).
	// operationName must project from operationType; operationDescription stays nil.
	writeVertex(opKey, "meta", map[string]any{"operationType": "RecordIdentityPII"})
	writeVertex(targetKey, "leaseapp", map[string]any{"state": "pending"})
	writeVertex(taskKey, "task", map[string]any{
		"status": "open", "expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	// task = source (later-arriving), identity = target (Contract #1 §1.1). The
	// inbound assignedTo link is the mutation that MUST reproject the identity's row.
	writeLink("task", taskID, "assignedTo", "identity", identityID)
	writeLink("task", taskID, "forOperation", "meta", opID)
	writeLink("task", taskID, "scopedTo", "leaseapp", targetID)

	// --- assert the freshly-assigned task projects into the identity's inbox ---
	require.Eventually(t, func() bool {
		entry, gErr := myTasksKV.Get(ctx, expectedKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		if isDel, _ := env["isDeleted"].(bool); isDel {
			return false
		}
		tasks, _ := env["openTasks"].([]any)
		for _, e := range tasks {
			m, _ := e.(map[string]any)
			if m["taskKey"] == taskKey {
				return true
			}
		}
		return false
	}, 25*time.Second, 100*time.Millisecond,
		"freshly-assigned open task did not project into my-tasks (assignedTo not a reprojection trigger)")

	// Assert the row carries the link-walked, self-describing fields.
	entry, gErr := myTasksKV.Get(ctx, expectedKey)
	require.NoError(t, gErr)
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	tasks, _ := env["openTasks"].([]any)
	require.Len(t, tasks, 1)
	row, _ := tasks[0].(map[string]any)
	require.Equal(t, taskKey, row["taskKey"])
	require.Equal(t, opKey, row["forOperation"])
	// operationName projects from the op DDL's root operationType (the real-flow
	// source), NOT a manufactured .canonicalName aspect.
	require.Equal(t, "RecordIdentityPII", row["operationName"])
	require.Equal(t, targetKey, row["scopedTo"])
	// No .description aspect was authored on the op DDL → operationDescription is
	// null (best-effort), and the row still projects with a usable name.
	require.Nil(t, row["operationDescription"])
}
