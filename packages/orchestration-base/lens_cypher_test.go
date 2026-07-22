package orchestrationbase

// Rule-engine proof of the unroutedTasks convergence lens (FR29), driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-reminders/lease-signing use.
//
//   - QUEUED, NOT YET EXPIRED: not violating; freshUntil = expiresAt (arms the
//     @at timer) — still time to claim it.
//   - QUEUED, EXPIRED (never claimed): violating; missing_claim true;
//     freshUntil null — the row itself drives dispatch from here.
//   - DIRECTLY ASSIGNED (no queuedFor at all): the required -[:queuedFor]->
//     match never fires — zero rows, so the task never gets a weaver-targets
//     entry in the first place.
//   - CLAIMED (assignedTo, queuedFor tombstoned): same zero-rows outcome as
//     direct assignment — ClaimTask's atomic swap makes the row disappear on
//     the next reprojection (EmptyBehavior:"delete" removes any prior row).
//   - CANCELLED while still queued: the WHERE status='open' gate excludes it —
//     zero rows.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The injected projection instant for every case below.
const unrNow = "2026-06-30T12:00:00Z"

func unrCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-unr-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-unr-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-unr-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-unr-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func unrNanoID(name string) string {
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

type unrFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newUnrFixture(t *testing.T) *unrFixture {
	adjKV, coreKV := unrCypherKVs(t)
	return &unrFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *unrFixture) vtx(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := unrNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	if data == nil {
		data = map[string]any{}
	}
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *unrFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// projectAt runs the anchored unroutedTasks spec for one task with an
// INJECTED $now (the same param executeFullForActor supplies live).
func (f *unrFixture) projectAt(t *testing.T, taskName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(unroutedTasksSpec)
	require.NoError(t, err, "unroutedTasks cypher must parse on the full engine")
	taskKey := "vtx.task." + f.ids[taskName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    taskKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestUnroutedTasks_QueuedNotYetExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	rows := f.projectAt(t, "task1", unrNow)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.task."+f.ids["task1"], v["entityKey"])
	require.Equal(t, false, v["missing_claim"], "expiresAt is still in the future — not stale")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"], "freshUntil = expiresAt arms the @at timer")
	require.Equal(t, "vtx.role."+f.ids["queueRole"], v["queuedRole"])
}

func TestUnroutedTasks_QueuedExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	v := f.projectAt(t, "task1", unrNow)[0].Values
	require.Equal(t, true, v["missing_claim"], "expiresAt passed while still queued — never claimed")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already stale → no future deadline → no armed timer")
}

func TestUnroutedTasks_DirectlyAssignedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")

	rows := f.projectAt(t, "task1", unrNow)
	require.Empty(t, rows, "a direct-assigned task carries no queuedFor link, so the required MATCH never fires")
}

func TestUnroutedTasks_ClaimedTaskNoLongerMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	// ClaimTask's atomic swap: queuedFor is gone, assignedTo(claimant) is the
	// only relationship left — the same shape as a direct assignment.
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "claimant", "identity", nil)
	f.edge(t, "assignedTo", "task1", "claimant")

	rows := f.projectAt(t, "task1", unrNow)
	require.Empty(t, rows, "post-ClaimTask the queuedFor link is gone — the row disappears (EmptyBehavior:delete)")
}

func TestUnroutedTasks_CancelledWhileQueuedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "cancelled", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	rows := f.projectAt(t, "task1", unrNow)
	require.Empty(t, rows, "a cancelled task is excluded by the status='open' gate even if queuedFor lingers")
}
