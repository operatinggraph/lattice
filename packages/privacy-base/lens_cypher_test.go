package privacybase

// Rule-engine proof of the shredStatus lens (Fire 4b): drives the spec
// through the `full` engine — the engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV (the same
// harness clinic-domain / lease-signing use for their lens cypher tests).
//
// What it proves the unit/structure tests cannot:
//   - the boolean WHERE (`i.piiKey.data.shredded = true`) keeps un-shredded
//     piiKey holders and piiKey-less identities OUT — the read model is a
//     shred ledger, not a key inventory;
//   - the null-safe aspect-hops project null for not-yet-recorded
//     finalization steps (the "in flight" rendering) and true once recorded.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func shredCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-shred-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-shred-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-shred-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-shred-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func putShredVtx(t *testing.T, coreKV *substrate.KV, id string, piiKeyData map[string]any) string {
	t.Helper()
	ctx := context.Background()
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := coreKV.Put(ctx, key, raw)
	require.NoError(t, err)
	if piiKeyData != nil {
		aKey := key + ".piiKey"
		aBody := map[string]any{"key": aKey, "class": "piiKey", "vertexKey": key, "localName": "piiKey", "isDeleted": false, "data": piiKeyData}
		aRaw, _ := json.Marshal(aBody)
		_, err = coreKV.Put(ctx, aKey, aRaw)
		require.NoError(t, err)
	}
	return key
}

func TestShredStatusLens_ProjectsOnlyShreddedIdentities(t *testing.T) {
	adjKV, coreKV := shredCypherKVs(t)

	// In-flight shred: shredded, neither finalization step recorded yet.
	inflightKey := putShredVtx(t, coreKV, "AAshredInFlightAAAAA", map[string]any{
		"wrappedDEK": "abc", "shredded": true, "shreddedAt": "2026-07-02T10:10:00Z",
	})
	// Fully finalized shred: both steps recorded.
	doneKey := putShredVtx(t, coreKV, "AAshredFinalizedAAAA", map[string]any{
		"wrappedDEK": "def", "shredded": true, "shreddedAt": "2026-07-02T10:11:00Z",
		"vaultKeyDestroyed": true, "vaultKeyDestroyedAt": "2026-07-02T10:12:00Z",
		"projectionsNullified": true, "projectionsNullifiedAt": "2026-07-02T10:13:00Z",
	})
	// Excluded: an unshredded piiKey holder and a piiKey-less identity.
	putShredVtx(t, coreKV, "AAshredUnshreddedAAA", map[string]any{"wrappedDEK": "ghi", "shredded": false})
	putShredVtx(t, coreKV, "AAshredNoPiiKeyAAAAA", nil)

	eng := full.New()
	cr, err := eng.Parse(shredStatusSpec)
	require.NoError(t, err, "shredStatus cypher must parse on the full engine")
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)

	byKey := map[string]ruleengine.ProjectionResult{}
	for _, r := range rows {
		k, _ := r.Values["key"].(string)
		byKey[k] = r
	}
	require.Len(t, byKey, 2, "only the two SHREDDED identities may project; got %v", byKey)

	inflight := byKey[inflightKey].Values
	require.Equal(t, true, inflight["shredded"])
	require.Nil(t, inflight["vaultKeyDestroyed"], "not-yet-recorded step must project null (in flight)")
	require.Nil(t, inflight["projectionsNullified"])

	done := byKey[doneKey].Values
	require.Equal(t, true, done["vaultKeyDestroyed"])
	require.Equal(t, true, done["projectionsNullified"])
	require.Equal(t, "2026-07-02T10:12:00Z", done["vaultKeyDestroyedAt"])
	require.Equal(t, "2026-07-02T10:13:00Z", done["projectionsNullifiedAt"])
}
