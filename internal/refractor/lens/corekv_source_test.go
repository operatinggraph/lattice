package lens_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/substrate"
)

// TestCoreKVSource_LoadsLensFromAspect verifies that when a
// `vtx.meta.<id>` vertex with envelope class `meta.lens` plus its
// `vtx.meta.<id>.spec` aspect are written to Core KV, the CoreKVSource
// translates them into a *Rule and invokes the load callback. This is
// the AC #3 path: "Lens activation flows through the standard Processor
// write path" (data-contracts.md §1.2 line 70).
func TestCoreKVSource_LoadsLensFromAspect(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	// Start the CoreKVSource and register callbacks.
	src := lens.NewCoreKVSource(conn, "core-kv", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	var updateMu sync.Mutex
	var updates []*lens.Rule
	src.SetUpdateCallback(func(_, n *lens.Rule, _ lens.UpdateKind) {
		updateMu.Lock()
		updates = append(updates, n)
		updateMu.Unlock()
	})
	require.NoError(t, src.Start(ctx))

	// Write the meta-lens vertex first (vtx.meta.<id> with class "meta.lens").
	vtxKey := "vtx.meta.AbCdEfGhJkMnPqRsTuVw"
	require.NoError(t, putJSON(ctx, kv, vtxKey, map[string]any{"id": "AbCdEfGhJkMnPqRsTuVw", "class": "meta.lens"}))

	// Now write the spec aspect.
	spec := lens.LensSpec{
		ID:            "AbCdEfGhJkMnPqRsTuVw",
		CanonicalName: "lens.contract-view",
		TargetType:    "nats_kv",
		CypherRule:    "MATCH (c:contract) RETURN c.id AS contract_id",
		TargetConfig:  json.RawMessage(`{"bucket":"contract_view","key":["contract_id"]}`),
	}
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", specJSON))

	select {
	case r := <-loaded:
		require.Equal(t, "AbCdEfGhJkMnPqRsTuVw", r.ID)
		require.Equal(t, "nats_kv", r.Into.Target)
		require.Equal(t, "contract_view", r.Into.Bucket)
		require.Equal(t, "MATCH (c:contract) RETURN c.id AS contract_id", r.Match)
	case <-time.After(3 * time.Second):
		t.Fatal("load callback not invoked within 3s")
	}

	// Now update the spec — should fire updateCB, not loadCB again.
	spec.CypherRule = "MATCH (c:contract) RETURN c.id AS contract_id, c.name AS name"
	specJSON, _ = json.Marshal(spec)
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", specJSON))

	require.Eventually(t, func() bool {
		updateMu.Lock()
		n := len(updates)
		updateMu.Unlock()
		return n >= 1
	}, 3*time.Second, 50*time.Millisecond, "update callback not invoked")
}

// TestCoreKVSource_LoadsLensFromAspect_SpecBeforeParent verifies the
// spec-before-parent buffering path: CDC ordering is not guaranteed, so a
// `.spec` aspect can arrive before its `vtx.meta.<id>` parent vertex. The
// spec must be buffered in pendingSpecs and replayed once the parent vertex
// (with class `meta.lens`) is observed — not dropped.
func TestCoreKVSource_LoadsLensFromAspect_SpecBeforeParent(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	src := lens.NewCoreKVSource(conn, "core-kv", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	require.NoError(t, src.Start(ctx))

	vtxKey := "vtx.meta.ZzYyXxWwVvUuTtSsRrQq"

	// Write the spec aspect FIRST — before the parent vertex's class is known.
	spec := lens.LensSpec{
		ID:            "ZzYyXxWwVvUuTtSsRrQq",
		CanonicalName: "lens.spec-before-parent",
		TargetType:    "nats_kv",
		CypherRule:    "MATCH (c:contract) RETURN c.id AS contract_id",
		TargetConfig:  json.RawMessage(`{"bucket":"spec_before_parent_view","key":["contract_id"]}`),
	}
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", specJSON))

	// Give the source a moment to (not) process the orphaned spec — it must
	// buffer it, not drop it or dispatch prematurely.
	select {
	case r := <-loaded:
		t.Fatalf("load callback fired before parent vertex arrived: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}

	// Now write the parent vertex — the buffered spec must replay.
	require.NoError(t, putJSON(ctx, kv, vtxKey, map[string]any{"id": "ZzYyXxWwVvUuTtSsRrQq", "class": "meta.lens"}))

	select {
	case r := <-loaded:
		require.Equal(t, "ZzYyXxWwVvUuTtSsRrQq", r.ID)
		require.Equal(t, "spec_before_parent_view", r.Into.Bucket)
	case <-time.After(3 * time.Second):
		t.Fatal("buffered spec was not replayed after parent vertex arrived")
	}
}

// TestCoreKVSource_SkipsEventStreamSpec verifies that a lens spec declaring
// `source.kind: "eventStream"` (a Chronicler-owned definition, e.g.
// orchestration-base's loomFlowHistory) is silently skipped rather than
// dispatched to translateSpec — which would otherwise fail every time with
// "cypherRule required" (an eventStream spec never carries a cypherRule)
// and spam an ERROR log on every restart / Core-KV replay
// (chronicler-host-reconciliation Increment 2).
func TestCoreKVSource_SkipsEventStreamSpec(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	src := lens.NewCoreKVSource(conn, "core-kv", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	require.NoError(t, src.Start(ctx))

	vtxKey := "vtx.meta.EvStrmLensAbCdEfGhJk"
	require.NoError(t, putJSON(ctx, kv, vtxKey, map[string]any{"id": "EvStrmLensAbCdEfGhJk", "class": "meta.lens"}))

	specBody := map[string]any{
		"id":            "EvStrmLensAbCdEfGhJk",
		"canonicalName": "lens.loomFlowHistory",
		"targetType":    "nats_kv",
		"targetConfig":  map[string]any{"bucket": "orchestration-history", "key": []string{"instanceId"}},
		"source": map[string]any{
			"kind":     "eventStream",
			"subjects": []string{"events.loom.>"},
			"project": map[string]any{
				"key":     "targetKey",
				"columns": map[string]any{"instanceId": "targetKey"},
			},
		},
	}
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", specBody))

	// A coreKv lens written right after must still load normally — the skip
	// must be specific to eventStream, not a wholesale dispatchSpec break.
	coreKvKey := "vtx.meta.CoreKvLensAbCdEfGhJk"
	require.NoError(t, putJSON(ctx, kv, coreKvKey, map[string]any{"id": "CoreKvLensAbCdEfGhJk", "class": "meta.lens"}))
	coreKvSpec := lens.LensSpec{
		ID:           "CoreKvLensAbCdEfGhJk",
		TargetType:   "nats_kv",
		CypherRule:   "MATCH (c:contract) RETURN c.id AS contract_id",
		TargetConfig: json.RawMessage(`{"bucket":"contract_view","key":["contract_id"]}`),
	}
	coreKvSpecJSON, err := json.Marshal(coreKvSpec)
	require.NoError(t, err)
	require.NoError(t, putJSON(ctx, kv, coreKvKey+".spec", coreKvSpecJSON))

	select {
	case r := <-loaded:
		require.Equal(t, "CoreKvLensAbCdEfGhJk", r.ID, "the eventStream lens must never reach loadCB")
	case <-time.After(3 * time.Second):
		t.Fatal("coreKv load callback not invoked within 3s")
	}

	select {
	case r := <-loaded:
		t.Fatalf("eventStream lens %q unexpectedly reached loadCB", r.ID)
	case <-time.After(300 * time.Millisecond):
		// Expected: nothing more arrives.
	}
}

func putJSON(ctx context.Context, kv jetstream.KeyValue, key string, value any) error {
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case json.RawMessage:
		data = v
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return err
		}
	}
	_, err := kv.Put(ctx, key, data)
	return err
}

// TestBootstrapLens_Disabled verifies that the env var gates the lens.
func TestBootstrapLens_Disabled(t *testing.T) {
	t.Setenv(lens.BootstrapLensEnvVar, "")
	require.False(t, lens.BootstrapEnabled())
}

// TestBootstrapLens_Enabled verifies activation and shape.
func TestBootstrapLens_Enabled(t *testing.T) {
	t.Setenv(lens.BootstrapLensEnvVar, "1")
	require.True(t, lens.BootstrapEnabled())
	l := lens.BootstrapLens()
	require.Equal(t, lens.BootstrapLensNanoID, l.ID)
	require.Equal(t, "postgres", l.Into.Target)
	require.Equal(t, "contract_view", l.Into.Table)
}
