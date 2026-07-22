// Package refractor_test — end-to-end proof for personal-secure-lens-design.md
// Fire 1 (PL.1): a lens targeting "nats_subject" fans a Core KV mutation to
// its recipient's per-identity subject through the real CDC pipeline (Core
// KV write → CoreKVSource lens activation → pipeline → NatsSubjectAdapter →
// lattice.sync.user.<actor>), under the trusted-single-identity posture (no
// security filter, no Interest Set, no per-actor fan-out enumerator — those
// are later fires).
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

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPersonalLens_PL1_E2E_MutationFansToActorSubject is PL.1's own stated
// green bar (personal-secure-lens-design.md §7): a lens configured with
// target "nats_subject" projects a Core KV mutation to
// lattice.sync.user.<actor>, subscribed and asserted by a real consumer.
func TestPersonalLens_PL1_E2E_MutationFansToActorSubject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}

	const (
		coreBucket    = "core-kv"
		adjBucket     = "refractor-adjacency"
		subjectPrefix = "lattice.sync.user"
		syncStream    = "SYNC"
		recipient     = "identityA"
	)
	lensID := e2eContractNanoID(2) // 20-char NanoID, alphabet-valid (opaque id; canonicalName below names it)

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: coreBucket})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: adjBucket})
	require.NoError(t, err)
	coreKV, err := conn.OpenKV(ctx, coreBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, adjBucket)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	boots := consumer.NewBootstrapper(conn, coreBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// A single-vertex-anchored lens (no cross-vertex fan-out — that's Fire
	// 2's ActorEnumerator): the mutated lease vertex's own ownerId column is
	// aliased __actor, proving the transport end-to-end under the trusted-
	// single-identity posture.
	eng := full.New()
	cr, err := eng.Parse("MATCH (l:lease {key: $actorKey}) RETURN l.ownerId AS __actor, l.id AS entityId, l.monthlyRent AS monthlyRent")
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = []string{adapter.PersonalActorKeyField, "entityId"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt, err := adapter.NewNatsSubjectAdapter(ctx, conn, "rule-1", subjectPrefix, syncStream, []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	p, err := pipeline.New(lensID, "nats_subject", coreBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	p.RunOn(conn, e2eSpec(lensID, coreBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	go p.Run(pipelineCtx)
	t.Cleanup(pipelineCancel)

	src := lens.NewCoreKVSource(conn, coreBucket, "test", logger)
	lensActivated := make(chan struct{}, 1)
	src.SetLoadCallback(func(_ *lens.Rule) {
		select {
		case lensActivated <- struct{}{}:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	metaVertexKey := "vtx.meta." + lensID
	specKey := metaVertexKey + ".spec"
	vertexJSON, _ := json.Marshal(map[string]any{"class": "meta.lens", "key": metaVertexKey, "data": map[string]any{}})
	_, err = coreKV.Put(ctx, metaVertexKey, vertexJSON)
	require.NoError(t, err)

	spec := lens.LensSpec{
		ID:            lensID,
		CanonicalName: "lens.e2e-personal-lens-pl1",
		TargetType:    "nats_subject",
		CypherRule:    "MATCH (l:lease {key: $actorKey}) RETURN l.ownerId AS __actor, l.id AS entityId, l.monthlyRent AS monthlyRent",
		TargetConfig:  json.RawMessage(`{"subjectPrefix":"` + subjectPrefix + `","stream":"` + syncStream + `","key":["__actor","entityId"]}`),
	}
	specJSON, _ := json.Marshal(spec)
	_, err = coreKV.Put(ctx, specKey, specJSON)
	require.NoError(t, err)

	select {
	case <-lensActivated:
	case <-time.After(5 * time.Second):
		t.Fatal("CoreKVSource did not activate the lens within 5s of writes")
	}

	// Subscribe a real consumer on the actor's subject BEFORE the mutation
	// lands, mirroring how an Edge device would already be subscribed.
	cons, err := js.CreateOrUpdateConsumer(ctx, syncStream, jetstream.ConsumerConfig{
		FilterSubject: subjectPrefix + "." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	leaseNanoID := e2eContractNanoID(1)
	leaseKey := "vtx.lease." + leaseNanoID
	leaseBody, _ := json.Marshal(map[string]any{
		"id":             "lease-e2e-1",
		"ownerId":        recipient,
		"monthlyRent":    2400,
		"isDeleted":      false,
		"createdAt":      time.Now().UTC().Format(time.RFC3339),
		"lastModifiedAt": time.Now().UTC().Format(time.RFC3339),
	})
	_, err = coreKV.Put(ctx, leaseKey, leaseBody)
	require.NoError(t, err)

	msg, err := cons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "must receive one delta envelope on %s.%s", subjectPrefix, recipient)

	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "upsert", env["op"])
	require.Equal(t, "lease-e2e-1", env["key"])
	data, ok := env["data"].(map[string]any)
	require.True(t, ok, "envelope must carry a data payload")
	require.Equal(t, float64(2400), data["monthlyRent"])
}
