// my-tasks lens e2e: a per-identity OPEN-task projection. When a task leaves
// `open`, the envelope wrapper's ErrDeleteProjection drives a guarded delete,
// which the monotonic projection-write guard records as a soft tombstone
// {isDeleted:true, projectionSeq} (Contract #6 §6.2, Contract #10 §10.1).
// Absence and an isDeleted tombstone are equivalent for the reader: neither
// surfaces the closed task. The guard makes the close authoritative — a stale
// open-era re-projection carries a lower stream sequence and is rejected, so it
// cannot resurrect the closed task. The lens reuses the proven ephemeral-lens
// architecture (link-sourced cypher + actor fan-out + descriptor-driven delete),
// so this test exercises the package's myTasks cypher + its §6.13 Output
// descriptor end-to-end through the real pipeline.
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

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

func TestRefractor_MyTasksLens_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping my-tasks e2e test in -short mode")
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

	// my-tasks is a package-owned bucket (NOT primordial); create it the way
	// the refractor lazily would.
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

	// Compile the package's myTasks cypher and wire a pipeline exactly as
	// cmd/refractor's data-driven actor-aggregate path does.
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
	// my-tasks is a guarded per-actor projection: a close becomes a soft
	// tombstone carrying the watermark, and a stale lower-seq replay is rejected
	// (Contract #6 §6.2, Contract #10 §10.1).
	adpt.SetGuarded(true)

	const lensID = "MyTasksLensId0000001"
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

	// --- fixture: identity + open task + the three links ---
	identityID := stableNanoID("mytasks-assignee")
	taskID := stableNanoID("mytasks-task")
	opID := stableNanoID("mytasks-op")
	targetID := stableNanoID("mytasks-target")

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

	writeVertex(opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	// The op's name is its root operationType (the lens projects operationName from
	// it); its optional human instructions live in a .description aspect the lens
	// best-effort hops to project a self-describing row.
	writeVertex(opKey+".description", "aspect", map[string]any{"value": "Approve the pending lease application."})
	writeVertex(targetKey, "leaseapp", map[string]any{"state": "pending"})
	writeVertex(taskKey, "task", map[string]any{
		"status": "open", "expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	// task = source (later-arriving), the other vertex = target (Contract #1 §1.1).
	buildEdge("assignedTo", "task", taskID, "identity", identityID)
	buildEdge("forOperation", "task", taskID, "meta", opID)
	buildEdge("scopedTo", "task", taskID, "leaseapp", targetID)

	// Finally write the identity vertex — the CDC event the lens projects on.
	writeVertex(identityKey, "identity", map[string]any{"name": "assignee"})

	expectedKey := myTasksDesc.BuildKey(identityKey)

	// --- assert the open task projects a my-tasks row for its assignee ---
	require.Eventually(t, func() bool {
		entry, gErr := myTasksKV.Get(ctx, expectedKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
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
	}, 25*time.Second, 100*time.Millisecond, "open task did not project into my-tasks")

	// Assert the projected row carries the link-walked fields.
	entry, gErr := myTasksKV.Get(ctx, expectedKey)
	require.NoError(t, gErr)
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	require.Equal(t, identityKey, env["assignee"])
	tasks, _ := env["openTasks"].([]any)
	require.Len(t, tasks, 1)
	row, _ := tasks[0].(map[string]any)
	require.Equal(t, taskKey, row["taskKey"])
	require.Equal(t, opKey, row["forOperation"])
	require.Equal(t, "ApproveLeaseApplication", row["operationName"])
	require.Equal(t, "Approve the pending lease application.", row["operationDescription"])
	require.Equal(t, targetKey, row["scopedTo"])

	// Capture the open-era watermark so the close-era tombstone can be asserted
	// to carry a strictly-greater projectionSeq.
	openEntry, gErr := myTasksKV.Get(ctx, expectedKey)
	require.NoError(t, gErr)
	var openEnv map[string]any
	require.NoError(t, json.Unmarshal(openEntry.Value, &openEnv))
	openSeq, _ := openEnv["projectionSeq"].(float64)

	// --- vanish-on-close: flip the task to complete; the guarded key must
	// become a soft tombstone (not absent). The monotonic projection-write guard
	// makes the close authoritative: a still-in-flight open re-projection carries
	// a lower stream sequence and is rejected, so the key cannot be resurrected.
	writeVertex(taskKey, "task", map[string]any{
		"status": "complete", "expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})

	require.Eventually(t, func() bool {
		entry, err := myTasksKV.Get(ctx, expectedKey)
		if err != nil {
			return false // the guarded delete tombstones; the key must remain present
		}
		var env map[string]any
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			return false
		}
		isDeleted, _ := env["isDeleted"].(bool)
		seq, _ := env["projectionSeq"].(float64)
		return isDeleted && seq >= openSeq
	}, 25*time.Second, 100*time.Millisecond,
		"closed task must become an isDeleted tombstone carrying a watermark ≥ the open-era seq")
}
