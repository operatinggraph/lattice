package projection_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestSoftDelete_ReusesGuardedTombstone proves AC7: emptyBehavior: softDelete
// maps onto the SAME 12.1a guarded soft-tombstone in natskv.go — a guarded
// Delete writes {isDeleted:true, projectionSeq}. The projection layer signals
// the empty result; the writer dispatches the existing guarded Delete; there is
// no second tombstone path.
func TestSoftDelete_ReusesGuardedTombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	kv := startTargetKV(t)
	a, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	a.SetGuarded(true) // the actor-aggregate (auth) lenses enable the guard.

	desc := projection.OutputDescriptor{
		AnchorType:       "identity",
		OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
		BodyColumns:      []string{"ephemeralGrants"},
		EmptyBehavior:    projection.EmptySoftDelete,
		RealnessFilter:   "taskKey",
		Freshness:        projection.FreshnessAuto,
	}

	if !desc.RequiresGuardedTombstone() {
		t.Fatalf("softDelete must require the guarded tombstone")
	}
	if desc.EmptyAction() != projection.ActionSoftDelete {
		t.Fatalf("softDelete must map to ActionSoftDelete")
	}

	// The empty-result key the writer derives from the descriptor for an actor.
	actorKey := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	outKey := desc.BuildKey(actorKey)
	if outKey != "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("BuildKey: %q", outKey)
	}

	// Dispatch the empty-result action through the EXISTING guarded Delete path.
	const seq = uint64(42)
	if err := a.Delete(context.Background(), map[string]any{"key": outKey}, seq); err != nil {
		t.Fatalf("guarded delete: %v", err)
	}

	// Read back: the existing mechanism wrote {isDeleted:true, projectionSeq}.
	entry, err := kv.Get(context.Background(), outKey)
	if err != nil {
		t.Fatalf("get tombstone: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if isDeleted, _ := doc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("expected isDeleted:true tombstone, got %v", doc)
	}
	if ps, _ := doc["projectionSeq"].(float64); uint64(ps) != seq {
		t.Fatalf("expected projectionSeq=%d, got %v", seq, doc["projectionSeq"])
	}
}

func startTargetKV(t *testing.T) *substrate.KV {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "proj-target"}); err != nil {
		t.Fatalf("kv: %v", err)
	}
	kv, err := conn.OpenKV(ctx, "proj-target")
	if err != nil {
		t.Fatalf("open kv: %v", err)
	}
	return kv
}
