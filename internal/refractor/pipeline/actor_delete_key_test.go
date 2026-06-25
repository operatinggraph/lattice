package pipeline

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/substrate"
)

// ephemeralDeleteKey mirrors capabilityenv.EphemeralKey; the producer wires the
// real function in cmd/refractor. Duplicated here only to keep the pipeline
// package's test free of an import cycle (capabilityenv imports pipeline).
func ephemeralDeleteKey(actorKey string) string {
	const vtxPrefix = "vtx."
	if len(actorKey) > len(vtxPrefix) && actorKey[:len(vtxPrefix)] == vtxPrefix {
		return "cap.ephemeral." + actorKey[len(vtxPrefix):]
	}
	return "cap.ephemeral." + actorKey
}

// newDeleteKeyKV stands up an in-memory NATS server with empty Core/Adj KV
// buckets so the missing-actor reprojection path resolves a real
// ErrKeyNotFound.
func newDeleteKeyKV(t *testing.T) (coreKV, adjKV *substrate.KV) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "CORE"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJ"})
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "CORE")
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	return coreKV, adjKV
}

func newDeleteKeyPipeline(t *testing.T, deleteKey func(string) string) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	p := &Pipeline{
		ruleID:          "test-rule",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
	}
	if deleteKey != nil {
		p.actorDeleteKey = deleteKey
	}
	return p
}

// onlyDelete asserts a single Delete result and returns its target key.
func onlyDelete(t *testing.T, results []simple.EvalResult) string {
	t.Helper()
	require.Len(t, results, 1)
	require.True(t, results[0].Delete)
	key, _ := results[0].Keys["key"].(string)
	return key
}

const (
	deleteKeyNanoID = "Tdek1JdentityAaaaaaa"
	deleteKeyActor  = "vtx.identity." + deleteKeyNanoID
)

func TestActorTombstone_EphemeralDeleteKey(t *testing.T) {
	p := newDeleteKeyPipeline(t, ephemeralDeleteKey)
	results, err := p.evaluateForEntry(context.Background(), simple.NodeEntry{
		CoreKVKey: deleteKeyActor,
		NodeLabel: "identity",
		IsDeleted: true,
	})
	require.NoError(t, err)
	require.Equal(t, "cap.ephemeral.identity."+deleteKeyNanoID, onlyDelete(t, results))
}

func TestReprojectActors_MissingActor_EphemeralDeleteKey(t *testing.T) {
	p := newDeleteKeyPipeline(t, ephemeralDeleteKey)
	// Actor absent from Core KV → missing-actor branch emits the Delete.
	results, err := p.reprojectActors(context.Background(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.Equal(t, "cap.ephemeral.identity."+deleteKeyNanoID, onlyDelete(t, results))
}

func TestActorTombstone_DefaultDeleteKey_Unchanged(t *testing.T) {
	// No actorDeleteKey installed → primary capability lens behaviour: cap.<actor>.
	p := newDeleteKeyPipeline(t, nil)
	results, err := p.evaluateForEntry(context.Background(), simple.NodeEntry{
		CoreKVKey: deleteKeyActor,
		NodeLabel: "identity",
		IsDeleted: true,
	})
	require.NoError(t, err)
	require.Equal(t, "cap.identity."+deleteKeyNanoID, onlyDelete(t, results))
}

func TestReprojectActors_MissingActor_DefaultDeleteKey_Unchanged(t *testing.T) {
	p := newDeleteKeyPipeline(t, nil)
	results, err := p.reprojectActors(context.Background(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.Equal(t, "cap.identity."+deleteKeyNanoID, onlyDelete(t, results))
}
