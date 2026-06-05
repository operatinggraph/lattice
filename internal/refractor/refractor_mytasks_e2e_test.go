// my-tasks lens e2e: a per-identity OPEN-task projection that genuinely
// vanishes when the task leaves `open` (the 7.1 FIX-1 absence mechanism via the
// envelope wrapper's ErrDeleteProjection). The lens reuses the proven
// ephemeral-lens architecture (link-sourced cypher + actor fan-out + wrapper-
// driven delete), so this test exercises the package's myTasks cypher + the
// NewMyTasksWrapper end-to-end through the real pipeline.
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
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

func TestRefractor_MyTasksLens_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping my-tasks e2e test in -short mode")
	}

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()}
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

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := js.KeyValue(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)

	// my-tasks is a package-owned bucket (NOT primordial); create it the way
	// the refractor lazily would.
	myTasksKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: orchestrationbase.MyTasksBucket})
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(js, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	manager := consumer.NewManager(js, bootstrap.CoreKVBucket)

	// Compile the package's myTasks cypher and wire a pipeline exactly as
	// cmd/refractor's startPipeline `case "myTasks"` does.
	fullEngine := full.New()
	var myTasksSpec string
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "myTasks" {
			myTasksSpec = l.Spec
		}
	}
	require.NotEmpty(t, myTasksSpec, "myTasks lens spec must exist")
	cr, err := fullEngine.Parse(myTasksSpec)
	require.NoError(t, err, "myTasks spec must parse")

	adpt, err := adapter.New(myTasksKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const lensID = "MyTasksLensId0000001"
	p, err := pipeline.New(lensID, "nats_kv", nil, bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(fullEngine, cr)
	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision()
	}
	p.SetEnvelopeFn(capabilityenv.NewMyTasksWrapper("vtx.meta."+lensID, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))
	p.SetActorDeleteKey(capabilityenv.MyTasksKey)

	require.NoError(t, manager.Add(ctx, lensID))
	cons := manager.Consumer(lensID)
	require.NotNil(t, cons)

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx, cons) }()
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

	expectedKey := capabilityenv.MyTasksKey(identityKey)

	// --- assert the open task projects a my-tasks row for its assignee ---
	require.Eventually(t, func() bool {
		entry, gErr := myTasksKV.Get(ctx, expectedKey)
		if gErr != nil || entry == nil || len(entry.Value()) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value(), &env) != nil {
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
	require.NoError(t, json.Unmarshal(entry.Value(), &env))
	require.Equal(t, identityKey, env["assignee"])
	tasks, _ := env["openTasks"].([]any)
	require.Len(t, tasks, 1)
	row, _ := tasks[0].(map[string]any)
	require.Equal(t, taskKey, row["taskKey"])
	require.Equal(t, opKey, row["forOperation"])
	require.Equal(t, targetKey, row["scopedTo"])

	// Workaround for a KNOWN, separately-tracked refractor projection-ordering
	// race: the natskv adapter writes projections unconditionally (no revision
	// guard / last-writer-wins), so a still-in-flight open re-projection of the
	// assignee can land AFTER the close and resurrect the key. The settle-wait
	// drains the open-era CDC fan-out backlog before flipping the task. This
	// masks the race for the test; it is NOT proof the race is closed.
	requireQuiescentRevision(t, ctx, myTasksKV, expectedKey)

	// --- vanish-on-close: flip the task to complete; the key must hard-delete ---
	writeVertex(taskKey, "task", map[string]any{
		"status": "complete", "expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})

	require.Eventually(t, func() bool {
		_, gErr := myTasksKV.Get(ctx, expectedKey)
		return gErr != nil // key absent → vanished on close
	}, 25*time.Second, 100*time.Millisecond,
		"closed task must vanish from my-tasks (FIX-1 genuine absence)")
}

// requireQuiescentRevision blocks until the key's KV revision stops advancing
// for a short settle window — i.e. no further re-projections are in flight.
// This drains the create-era CDC fan-out backlog so a stale open re-projection
// can't resurrect the key after a subsequent close.
func requireQuiescentRevision(t *testing.T, ctx context.Context, kv jetstream.KeyValue, key string) {
	t.Helper()
	const settle = 1500 * time.Millisecond
	deadline := time.Now().Add(25 * time.Second)
	var lastRev uint64
	stableSince := time.Time{}
	for time.Now().Before(deadline) {
		entry, err := kv.Get(ctx, key)
		var rev uint64
		if err == nil && entry != nil {
			rev = entry.Revision()
		}
		if rev != lastRev {
			lastRev = rev
			stableSince = time.Now()
		} else if !stableSince.IsZero() && time.Since(stableSince) >= settle {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("my-tasks key %s never reached a quiescent revision", key)
}
