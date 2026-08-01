package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// This file is the e2e-ish proof for docs/components/refractor.md's Lens
// lifecycle step 9: a lens tombstone (parent vtx.meta.<id> vertex deleted,
// or its .spec aspect deleted) must stop the running pipeline AND delete its
// durable JetStream consumer — never stranding a durable on the KV_core-kv
// stream just because no operator happened to call the "delete" control op.
// It drives the exact production trigger (lens.CoreKVSource's removal
// callback) end-to-end against a real embedded-NATS stream and asserts the
// durable's server-side presence/absence directly, mirroring
// internal/substrate's own ConsumerSupervisor.Remove tests
// (js.Consumer(...) + errors.Is(err, jetstream.ErrConsumerNotFound)).
//
// removalEntry is the minimal per-lens bookkeeping cmd/refractor's
// pipelineEntry carries for teardown — the pipeline handle plus cancel/wg —
// enough to exercise the same "remove the durable, THEN cancel, THEN wait"
// sequence cmd/refractor/delete.go's pipelineDeleter.Delete uses in
// production (see Pipeline.RemoveConsumer's doc for why that order is not
// optional), without pulling in the whole control.Service + main.go registry
// machinery — already covered by internal/refractor/control's own tests.
type removalEntry struct {
	pipeline *pipeline.Pipeline
	cancel   context.CancelFunc
	wg       *sync.WaitGroup
}

// TestPipeline_LensTombstone_StopsAndRemovesDurable activates two lenses via
// a real lens.CoreKVSource, tombstones one lens's parent meta-vertex, and
// asserts: the removal callback fires with the tombstoned lens's own ID; its
// pipeline is stopped (Run returns); its durable is gone from the stream;
// CoreKVSource and the local registry both stop serving it; and the
// NON-tombstoned sibling lens's pipeline, durable, and registration all
// survive untouched.
func TestPipeline_LensTombstone_StopsAndRemovesDurable(t *testing.T) {
	env := startPipelineEnv(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	const ruleA = "LensRmTombstoneAAAA1" // tombstoned
	const ruleB = "LensRmSibngBBBBBBBB1" // survives

	_, adptA := newTargetKV(t, env, "lens-remove-target-a", []string{"id"})
	_, adptB := newTargetKV(t, env, "lens-remove-target-b", []string{"id"})

	var mu sync.Mutex
	registry := make(map[string]*removalEntry)

	activate := func(r *lens.Rule) {
		eng, cr := compileFullRule(t, r.Match, []string(r.Into.Key))
		adpt := adptA
		if r.ID == ruleB {
			adpt = adptB
		}
		p, err := pipeline.New(r.ID, r.Into.Target, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
		require.NoError(t, err)
		p.UseFullEngine(eng, cr)
		cancel, wg := startPipeline(t, env, p, r.ID)
		mu.Lock()
		registry[r.ID] = &removalEntry{pipeline: p, cancel: cancel, wg: wg}
		mu.Unlock()
	}

	removed := make(chan string, 4)
	src := lens.NewCoreKVSource(env.conn, coreKVBucket, "test", logger)
	src.SetLoadCallback(activate)
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	src.SetRemoveCallback(func(old *lens.Rule) {
		mu.Lock()
		entry, ok := registry[old.ID]
		if ok {
			delete(registry, old.ID)
		}
		mu.Unlock()
		if !ok {
			return
		}
		// Mirrors cmd/refractor/delete.go's pipelineDeleter.Delete: remove the
		// durable BEFORE cancelling. Run's own shutdown stops the supervisor,
		// which clears its managed-consumer registry without deleting
		// anything, so a Remove attempted after cancel would silently no-op
		// and strand the durable — see Pipeline.RemoveConsumer's doc.
		require.NoError(t, entry.pipeline.RemoveConsumer(ctx))
		entry.cancel()
		entry.wg.Wait()
		removed <- old.ID
	})
	require.NoError(t, src.Start(ctx))

	putLens(t, env, ruleA, "lens.remove-a",
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS id",
		`{"bucket":"lens-remove-target-a","key":["id"]}`)
	putLens(t, env, ruleB, "lens.remove-b",
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS id",
		`{"bucket":"lens-remove-target-b","key":["id"]}`)

	pollUntil(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		_, okA := registry[ruleA]
		_, okB := registry[ruleB]
		return okA && okB
	})
	pollUntil(t, 5*time.Second, func() bool {
		return consumerExists(t, env, ruleA) && consumerExists(t, env, ruleB)
	})

	// Tombstone lens A's parent meta-vertex.
	require.NoError(t, env.coreKV.Delete(ctx, "vtx.meta."+ruleA))

	select {
	case id := <-removed:
		require.Equal(t, ruleA, id)
	case <-time.After(5 * time.Second):
		t.Fatal("remove callback did not fire within 5s of the tombstone")
	}

	require.False(t, consumerExists(t, env, ruleA), "tombstoned lens's durable must be deleted")
	require.True(t, consumerExists(t, env, ruleB), "sibling lens's durable must survive")

	_, okA := src.Get(ruleA)
	require.False(t, okA, "CoreKVSource must no longer serve the tombstoned lens")
	_, okB := src.Get(ruleB)
	require.True(t, okB, "CoreKVSource must still serve the surviving sibling")

	mu.Lock()
	_, stillA := registry[ruleA]
	_, stillB := registry[ruleB]
	mu.Unlock()
	require.False(t, stillA, "registry must no longer serve the tombstoned lens")
	require.True(t, stillB, "registry must still serve the surviving sibling")
}

// TestPipeline_LensSpecUpdate_DurableSurvives proves a spec UPDATE (the
// updateCB / hot-reload path) is never misclassified as a tombstone: an
// INTO-only edit must fire the update callback, never the removal callback,
// and both the durable and the running pipeline must survive it untouched.
func TestPipeline_LensSpecUpdate_DurableSurvives(t *testing.T) {
	env := startPipelineEnv(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	const ruleID = "LensRmUpdateSurviveA"
	_, adpt := newTargetKV(t, env, "lens-update-target", []string{"id"})

	activated := make(chan struct{}, 1)
	src := lens.NewCoreKVSource(env.conn, coreKVBucket, "test", logger)
	src.SetLoadCallback(func(r *lens.Rule) {
		eng, cr := compileFullRule(t, r.Match, []string(r.Into.Key))
		p, err := pipeline.New(r.ID, r.Into.Target, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
		require.NoError(t, err)
		p.UseFullEngine(eng, cr)
		startPipeline(t, env, p, r.ID)
		activated <- struct{}{}
	})
	updated := make(chan struct{}, 1)
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) { updated <- struct{}{} })
	removeFired := make(chan struct{}, 1)
	src.SetRemoveCallback(func(_ *lens.Rule) { removeFired <- struct{}{} })
	require.NoError(t, src.Start(ctx))

	putLens(t, env, ruleID, "lens.update-survives",
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS id",
		`{"bucket":"lens-update-target","key":["id"]}`)

	select {
	case <-activated:
	case <-time.After(5 * time.Second):
		t.Fatal("lens did not activate within 5s")
	}
	pollUntil(t, 5*time.Second, func() bool { return consumerExists(t, env, ruleID) })

	// INTO-only update: the MATCH clause is byte-identical, only targetConfig
	// changes — lens.ClassifyUpdate keys on the Match string alone, so this is
	// the hot-reload path, structurally disjoint from the two IsDeleted
	// branches that drive removal.
	putLens(t, env, ruleID, "lens.update-survives",
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS id",
		`{"bucket":"lens-update-target","key":["id"],"deleteMode":"soft"}`)

	select {
	case <-updated:
	case <-time.After(5 * time.Second):
		t.Fatal("update callback did not fire within 5s")
	}

	select {
	case <-removeFired:
		t.Fatal("remove callback must not fire for a spec update")
	case <-time.After(300 * time.Millisecond):
	}

	require.True(t, consumerExists(t, env, ruleID), "durable must survive a spec update")
	_, ok := src.Get(ruleID)
	require.True(t, ok, "CoreKVSource must still serve the updated (not removed) lens")
}

// consumerExists reports whether the named lens's durable currently exists on
// the Core KV backing stream.
func consumerExists(t *testing.T, env *pipelineEnv, ruleID string) bool {
	t.Helper()
	_, err := env.js.Consumer(context.Background(), "KV_"+coreKVBucket, "refractor-"+ruleID)
	if err == nil {
		return true
	}
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return false
	}
	t.Fatalf("unexpected error checking consumer %q: %v", ruleID, err)
	return false
}

// putLens writes a vtx.meta.<ruleID> vertex (class meta.lens) and its .spec
// aspect to Core KV — the standard lens-activation write shape CoreKVSource
// watches. Calling it again for the same ruleID with a different cypher/
// targetConfig is a spec UPDATE, not a re-create.
func putLens(t *testing.T, env *pipelineEnv, ruleID, canonicalName, cypher, targetConfig string) {
	t.Helper()
	ctx := context.Background()
	vtxKey := "vtx.meta." + ruleID
	vtxJSON, err := json.Marshal(map[string]any{"id": ruleID, "class": "meta.lens"})
	require.NoError(t, err)
	_, err = env.coreKV.Put(ctx, vtxKey, vtxJSON)
	require.NoError(t, err)

	spec := lens.LensSpec{
		ID:            ruleID,
		CanonicalName: canonicalName,
		TargetType:    "nats_kv",
		CypherRule:    cypher,
		TargetConfig:  json.RawMessage(targetConfig),
	}
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)
	_, err = env.coreKV.Put(ctx, vtxKey+".spec", specJSON)
	require.NoError(t, err)
}
