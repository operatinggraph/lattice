package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	kvs := newTestKVs(t, "CORE", "ADJ")
	return kvs[0], kvs[1]
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
func onlyDelete(t *testing.T, results []ruleengine.EvalResult) string {
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
	results, _, _, err := p.evaluateForEntry(context.Background(), p.ruleState(), ruleengine.NodeEntry{
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
	results, err := p.reprojectActors(context.Background(), p.ruleState(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.Equal(t, "cap.ephemeral.identity."+deleteKeyNanoID, onlyDelete(t, results))
}

func TestActorTombstone_DefaultDeleteKey_Unchanged(t *testing.T) {
	// No actorDeleteKey installed → primary capability lens behaviour: cap.<actor>.
	p := newDeleteKeyPipeline(t, nil)
	results, _, _, err := p.evaluateForEntry(context.Background(), p.ruleState(), ruleengine.NodeEntry{
		CoreKVKey: deleteKeyActor,
		NodeLabel: "identity",
		IsDeleted: true,
	})
	require.NoError(t, err)
	require.Equal(t, "cap.identity."+deleteKeyNanoID, onlyDelete(t, results))
}

func TestReprojectActors_MissingActor_DefaultDeleteKey_Unchanged(t *testing.T) {
	p := newDeleteKeyPipeline(t, nil)
	results, err := p.reprojectActors(context.Background(), p.ruleState(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.Equal(t, "cap.identity."+deleteKeyNanoID, onlyDelete(t, results))
}
