package projection_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/substrate"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

// This oracle re-runs the Story 12.2 spike's equivalence proof against the REAL
// production functions: simple.CompileInvalidationForest + the unexported
// reverseTraverse it calls via InvalidationForest.AffectedAnchors, and the real
// full.Engine.ExecuteWith reproject-and-diff. It compiles the LIVE lens specs
// (orchestration-base myTasksSpec / capabilityEphemeralSpec), not a snapshot.

func startKVs(t *testing.T) (*substrate.KV, *substrate.KV) {
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
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "proj-adj"}); err != nil {
		t.Fatalf("adj kv: %v", err)
	}
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "proj-core"}); err != nil {
		t.Fatalf("core kv: %v", err)
	}
	adjKV, err := conn.OpenKV(ctx, "proj-adj")
	if err != nil {
		t.Fatalf("open adj kv: %v", err)
	}
	coreKV, err := conn.OpenKV(ctx, "proj-core")
	if err != nil {
		t.Fatalf("open core kv: %v", err)
	}
	return adjKV, coreKV
}

func stableID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("proj:" + name) {
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

func vtxKey(typ, name string) string { return "vtx." + typ + "." + stableID(typ+":"+name) }

func putVertex(t *testing.T, kv *substrate.KV, typ, name string, data map[string]any) string {
	t.Helper()
	key := vtxKey(typ, name)
	putVertexRaw(t, kv, key, typ, data)
	return key
}

func putVertexRaw(t *testing.T, kv *substrate.KV, key, typ string, data map[string]any) {
	t.Helper()
	body := map[string]any{"key": key, "class": typ, "data": data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal vertex: %v", err)
	}
	if _, err := kv.Put(context.Background(), key, raw); err != nil {
		t.Fatalf("put vertex %s: %v", key, err)
	}
}

func putEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromName, toType, toName string) {
	t.Helper()
	fromID := stableID(fromType + ":" + fromName)
	toID := stableID(toType + ":" + toName)
	edgeID := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	mustBuild(t, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "outbound",
		NodeID: fromID, OtherNodeID: toID, OtherType: toType,
	})
	mustBuild(t, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "inbound",
		NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
	})
}

func removeEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromName, toType, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID := stableID(fromType + ":" + fromName)
	toID := stableID(toType + ":" + toName)
	edgeID := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	for _, e := range []adjacency.CoreKVEvent{
		{CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType, IsDeleted: true},
		{CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType, IsDeleted: true},
	} {
		if err := adjacency.Build(ctx, adjKV, e); err != nil {
			t.Fatalf("remove edge build: %v", err)
		}
	}
}

func mustBuild(t *testing.T, adjKV *substrate.KV, evt adjacency.CoreKVEvent) {
	t.Helper()
	if err := adjacency.Build(context.Background(), adjKV, evt); err != nil {
		t.Fatalf("adjacency build: %v", err)
	}
}

type fixture struct {
	adjKV, coreKV *substrate.KV
	mgrKey        string
	repKey        string
	bystanderKey  string
	taskDirectKey string
	taskRepKey    string
	opKey         string
	tgtKey        string
}

// buildFixture mirrors the spike fixture: a manager who inherits a report's
// task via reportsTo, a direct task, and a bystander sharing the op vertex via a
// non-lens relation (the over-reprojection case the BFS hits but the compiled
// directed plan does not).
func buildFixture(t *testing.T) *fixture {
	t.Helper()
	adjKV, coreKV := startKVs(t)
	f := &fixture{adjKV: adjKV, coreKV: coreKV}

	f.mgrKey = putVertex(t, coreKV, "identity", "mgr", map[string]any{"name": "mgr"})
	f.repKey = putVertex(t, coreKV, "identity", "rep", map[string]any{"name": "rep"})
	f.bystanderKey = putVertex(t, coreKV, "identity", "bystander", map[string]any{"name": "bystander"})

	future := float64(time.Now().Add(24 * time.Hour).Unix())
	f.taskDirectKey = putVertex(t, coreKV, "task", "direct", map[string]any{"status": "open", "expiresAt": future})
	f.taskRepKey = putVertex(t, coreKV, "task", "rep", map[string]any{"status": "open", "expiresAt": future})
	f.opKey = putVertex(t, coreKV, "op", "op1", map[string]any{"operationType": "approve"})
	f.tgtKey = putVertex(t, coreKV, "tgt", "tgt1", map[string]any{"name": "tgt1"})

	putEdge(t, adjKV, "reportsTo", "identity", "rep", "identity", "mgr")
	putEdge(t, adjKV, "assignedTo", "task", "direct", "identity", "mgr")
	putEdge(t, adjKV, "assignedTo", "task", "rep", "identity", "rep")
	putEdge(t, adjKV, "forOperation", "task", "direct", "op", "op1")
	putEdge(t, adjKV, "forOperation", "task", "rep", "op", "op1")
	putEdge(t, adjKV, "scopedTo", "task", "direct", "tgt", "tgt1")
	putEdge(t, adjKV, "watches", "identity", "bystander", "op", "op1")

	return f
}

func reproject(t *testing.T, f *fixture, body, actorKey string) string {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(body)
	if err != nil {
		t.Fatalf("reproject parse: %v", err)
	}
	params := map[string]any{"actorKey": actorKey, "now": float64(time.Now().Unix())}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, f.adjKV, f.coreKV)
	if err != nil {
		t.Fatalf("reproject execute (%s): %v", actorKey, err)
	}
	type row struct {
		Key    map[string]any
		Values map[string]any
	}
	rows := make([]row, 0, len(out))
	for _, r := range out {
		rows = append(rows, row{Key: r.Key, Values: r.Values})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("reproject marshal: %v", err)
	}
	return string(raw)
}

func changedOutputActors(t *testing.T, f *fixture, body string, bfs []string, mutate func()) map[string]struct{} {
	t.Helper()
	before := map[string]string{}
	for _, a := range bfs {
		before[a] = reproject(t, f, body, a)
	}
	mutate()
	changed := map[string]struct{}{}
	for _, a := range bfs {
		if reproject(t, f, body, a) != before[a] {
			changed[a] = struct{}{}
		}
	}
	return changed
}

func bfsActors(t *testing.T, f *fixture, eventKey, eventType string) map[string]struct{} {
	t.Helper()
	enum := pipeline.NewActorEnumerator(f.adjKV, f.coreKV, "identity")
	keys, err := enum.Enumerate(context.Background(), eventKey, eventType)
	if err != nil {
		t.Fatalf("BFS enumerate: %v", err)
	}
	return toSet(keys)
}

func compiledActors(t *testing.T, f *fixture, forest *simple.InvalidationForest, entry simple.NodeEntry) map[string]struct{} {
	t.Helper()
	keys, err := forest.AffectedAnchors(context.Background(), entry, f.adjKV)
	if err != nil {
		t.Fatalf("AffectedAnchors: %v", err)
	}
	return toSet(keys)
}

func toSet(keys []string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s[k] = struct{}{}
	}
	return s
}

func sortedKeys(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isSubset(sub, super map[string]struct{}) bool {
	for k := range sub {
		if _, ok := super[k]; !ok {
			return false
		}
	}
	return true
}

type event struct {
	name      string
	nodeLabel string
	nodeKey   string
	bfsSeeds  []seed
	mutate    func(t *testing.T, f *fixture)
}

type seed struct{ key, typ string }

func unionBFS(t *testing.T, f *fixture, seeds []seed) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range seeds {
		for a := range bfsActors(t, f, s.key, s.typ) {
			out[a] = struct{}{}
		}
	}
	return out
}

type lensCase struct {
	name string
	body string
}

// liveSpecs extracts the two live actor-aggregate lens cypher bodies from the
// installed orchestration-base package (the LIVE specs, per AC3).
func liveSpecs(t *testing.T) []lensCase {
	t.Helper()
	var cases []lensCase
	for _, l := range orchestrationbase.Lenses() {
		switch l.CanonicalName {
		case "myTasks", "capabilityEphemeral":
			cases = append(cases, lensCase{name: l.CanonicalName, body: l.Spec})
		}
	}
	if len(cases) != 2 {
		t.Fatalf("expected myTasks + capabilityEphemeral live specs, got %d", len(cases))
	}
	// Deterministic order: myTasks first.
	sort.Slice(cases, func(i, j int) bool { return cases[i].name < cases[j].name })
	return cases
}

func TestProjectionInvalidation_OracleHolds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	for _, lc := range liveSpecs(t) {
		lc := lc
		t.Run(lc.name, func(t *testing.T) {
			forest, err := simple.CompileInvalidationForest(lc.body)
			if err != nil {
				t.Fatalf("CompileInvalidationForest(%s): %v", lc.name, err)
			}
			if len(forest.Branches) == 0 {
				t.Fatalf("%s: compiled forest has no branches", lc.name)
			}
			for bi, p := range forest.Branches {
				if len(p.Steps) == 0 {
					t.Fatalf("%s: branch %d has no steps", lc.name, bi)
				}
			}

			strictSeen := false
			for _, mk := range eventMakers() {
				f := buildFixture(t)
				ev := mk(f)
				t.Run(ev.name, func(t *testing.T) {
					if runOracle(t, f, lc, forest, ev) {
						strictSeen = true
					}
				})
			}
			if !strictSeen {
				t.Errorf("%s: no event demonstrated a STRICT subset (compiled ⊊ BFS); over-reprojection win never exercised", lc.name)
			}
		})
	}
}

func runOracle(t *testing.T, f *fixture, lc lensCase, forest *simple.InvalidationForest, ev event) bool {
	t.Helper()

	bfs := unionBFS(t, f, ev.bfsSeeds)
	compiled := compiledActors(t, f, forest, simple.NodeEntry{CoreKVKey: ev.nodeKey, NodeLabel: ev.nodeLabel})

	if len(compiled) == 0 {
		t.Fatalf("[%s/%s] compiled set is EMPTY — a direction/edge-name mismatch would fake a subset pass", lc.name, ev.name)
	}
	if !isSubset(compiled, bfs) {
		t.Fatalf("[%s/%s] AC3(a) FAILED: compiled ⊄ BFS\n  compiled=%v\n  bfs=%v",
			lc.name, ev.name, sortedKeys(compiled), sortedKeys(bfs))
	}

	bfsList := sortedKeys(bfs)
	changed := changedOutputActors(t, f, lc.body, bfsList, func() { ev.mutate(t, f) })
	if !isSubset(changed, compiled) {
		t.Fatalf("[%s/%s] AC3(b) FAILED: MISSED ANCHOR — an actor whose output changed is NOT compiled\n  changed=%v\n  compiled=%v",
			lc.name, ev.name, sortedKeys(changed), sortedKeys(compiled))
	}

	win := len(bfs) - len(compiled)
	t.Logf("[%s/%s] |BFS|=%d |compiled|=%d win=%d changed-output=%d",
		lc.name, ev.name, len(bfs), len(compiled), win, len(changed))
	return win > 0
}

func eventMakers() []func(*fixture) event {
	return []func(*fixture) event{
		func(f *fixture) event {
			return event{
				name: "vertex_task", nodeLabel: "task", nodeKey: f.taskDirectKey,
				bfsSeeds: []seed{{f.taskDirectKey, "task"}},
				mutate: func(t *testing.T, f *fixture) {
					putVertexRaw(t, f.coreKV, f.taskDirectKey, "task",
						map[string]any{"status": "closed", "expiresAt": float64(1)})
				},
			}
		},
		func(f *fixture) event {
			return event{
				name: "link_assignedTo", nodeLabel: "task", nodeKey: f.taskRepKey,
				bfsSeeds: []seed{{f.taskRepKey, "task"}, {f.repKey, "identity"}},
				mutate: func(t *testing.T, f *fixture) {
					removeEdge(t, f.adjKV, "assignedTo", "task", "rep", "identity", "rep")
				},
			}
		},
		func(f *fixture) event {
			// A genuinely-distinct aspect mutation: the task stays OPEN and live
			// (not closed like vertex_task), only its expiresAt data field shifts
			// to a different future value. The field surfaces in both lenses'
			// projected rows, so the anchor's output changes — exercising the
			// changed ⊆ compiled assertion on a live-task aspect edit, not the
			// task-disappears path vertex_task covers.
			return event{
				name: "aspect_task_data", nodeLabel: "task", nodeKey: f.taskDirectKey,
				bfsSeeds: []seed{{f.taskDirectKey, "task"}},
				mutate: func(t *testing.T, f *fixture) {
					future := float64(time.Now().Add(72 * time.Hour).Unix())
					putVertexRaw(t, f.coreKV, f.taskDirectKey, "task",
						map[string]any{"status": "open", "expiresAt": future})
				},
			}
		},
		func(f *fixture) event {
			// An op-vertex (operationType) mutation. The op leaf is UNLABELED in
			// the live cypher ((task)-[:forOperation]->(op)), so its compiled
			// reverse-walk step carries ToLabel="". An empty ToLabel must match a
			// changed node of any label: the walk reverses op→task→identity and
			// reaches capabilityEphemeral's manager (whose projected operationType
			// changes). This event guards that leaf-vertex changes invalidate the
			// actor anchor — a label-gated step would skip "op", leave the compiled
			// set empty (runOracle's len==0 guard fires), and miss the manager.
			return event{
				name: "vertex_op_operationType", nodeLabel: "op", nodeKey: f.opKey,
				bfsSeeds: []seed{{f.opKey, "op"}},
				mutate: func(t *testing.T, f *fixture) {
					putVertexRaw(t, f.coreKV, f.opKey, "op",
						map[string]any{"operationType": "reject"})
				},
			}
		},
	}
}
