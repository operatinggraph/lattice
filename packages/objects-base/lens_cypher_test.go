package objectsbase

// Rule-engine proof of the objectLiveness convergence cypher (the v1b GC orphan
// detection). These drive objectLivenessSpec through the `full` rule engine
// directly — the same engine selected at activation via engine:"full" — against
// an embedded NATS Core/Adjacency KV, asserting the projection ROW.
//
// The load-bearing properties pinned here (§20):
//   - count(owner)=0 ⇒ orphaned; a live owner ⇒ not orphaned.
//   - DEAD-TARGET awareness: a live link to a TOMBSTONED owner does NOT hold the
//     object alive (count(owner) excludes the nil-bound dead owner) — the bug a
//     count(r)-based cypher would have.
//   - ONE ROW PER ANCHOR even with several links (the §0.C guard).
//   - linkEpoch (the object's root-data link-set version) is projected for the
//     reclaim op's epoch-CAS.

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
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func objCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-obj-cypher"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-obj-cypher"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-obj-cypher")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-obj-cypher")
	require.NoError(t, err)
	return adjKV, coreKV
}

func objCNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type objLensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newObjLensFixture(t *testing.T) *objLensFixture {
	adjKV, coreKV := objCypherKVs(t)
	return &objLensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

// object writes an object vertex with data.linkEpoch = epoch.
func (f *objLensFixture) object(t *testing.T, name string, epoch int) string {
	t.Helper()
	id := objCNanoID(name)
	f.ids[name] = id
	f.types[id] = "object"
	key := "vtx.object." + id
	body := map[string]any{"key": key, "class": "object", "isDeleted": false,
		"data": map[string]any{"linkEpoch": epoch}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// owner writes an owner (identity) vertex, live or tombstoned (the dead-target case).
func (f *objLensFixture) owner(t *testing.T, name string, deleted bool) string {
	t.Helper()
	id := objCNanoID(name)
	f.ids[name] = id
	f.types[id] = "identity"
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": deleted, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// link builds the object→owner adjacency edge (object is the source, Contract #1 §1.1).
func (f *objLensFixture) link(t *testing.T, name, objName, ownerName string) {
	t.Helper()
	ctx := context.Background()
	objID, ownerID := f.ids[objName], f.ids[ownerName]
	objType, ownerType := f.types[objID], f.types[ownerID]
	linkKey := "lnk." + objType + "." + objID + "." + name + "." + ownerType + "." + ownerID
	edgeID := name + "_" + objID + "_" + ownerID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: objID, OtherNodeID: ownerID, OtherType: ownerType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: ownerID, OtherNodeID: objID, OtherType: objType}))
}

func (f *objLensFixture) project(t *testing.T, objName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(objectLivenessSpec)
	require.NoError(t, err, "objectLiveness cypher must parse on the full engine")
	objKey := "vtx.object." + f.ids[objName]
	now := time.Now().UTC().Format(time.RFC3339)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": objKey, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// Test 1 — a live link to a live owner ⇒ not orphaned; linkEpoch projected.
func TestObjectLiveness_OneLiveLink_NotOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	objKey := f.object(t, "photo", 3)
	f.owner(t, "alice", false)
	f.link(t, "photoOf", "photo", "alice")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, objKey, v["entityKey"])
	require.Equal(t, false, v["missing_owner"], "a live link to a live owner ⇒ not orphaned")
	require.Equal(t, false, v["violating"])
	require.EqualValues(t, 3, v["linkEpoch"], "linkEpoch is projected for the reclaim CAS")
}

// Test 2 — zero links ⇒ orphaned (one null-restored row, not dropped).
func TestObjectLiveness_ZeroLinks_Orphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 5)

	rows := f.project(t, "photo")
	require.Len(t, rows, 1, "a zero-link object null-restores to exactly one row, not dropped")
	v := rows[0].Values
	require.Equal(t, true, v["missing_owner"], "zero live links ⇒ orphaned")
	require.Equal(t, true, v["violating"])
	require.EqualValues(t, 5, v["linkEpoch"])
}

// Test 3 — THE dead-target case: a live link to a TOMBSTONED owner does NOT hold
// the object alive (count(owner) excludes the nil-bound dead owner). A count(r)
// cypher would wrongly keep it alive — this is the M-2 regression guard.
func TestObjectLiveness_DeadTargetOwner_Orphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 2)
	f.owner(t, "ghost", true) // tombstoned owner
	f.link(t, "photoOf", "photo", "ghost")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_owner"], "a link to a TOMBSTONED owner must NOT hold the object alive")
	require.Equal(t, true, v["violating"])
}

// Test 4 — several live links ⇒ exactly one row (the one-row-per-anchor guard).
func TestObjectLiveness_MultipleLiveLinks_OneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 9)
	f.owner(t, "alice", false)
	f.owner(t, "bob", false)
	f.link(t, "photoOf", "photo", "alice")
	f.link(t, "avatarOf", "photo", "bob")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1, "exactly one row per object anchor even with several links")
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"])
	require.Equal(t, false, v["violating"])
}

// Test 5 — a dead owner alongside a live owner: the live one still counts, so the
// object is not orphaned (only the dead owner drops out).
func TestObjectLiveness_DeadOwnerPlusLiveOwner_NotOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 4)
	f.owner(t, "alice", false) // live
	f.owner(t, "ghost", true)  // tombstoned
	f.link(t, "photoOf", "photo", "alice")
	f.link(t, "photoOf", "photo", "ghost")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"], "one live owner ⇒ not orphaned (the dead owner drops out)")
	require.Equal(t, false, v["violating"])
}
