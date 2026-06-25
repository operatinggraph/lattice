package invalidationcompiler

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
)

// ---------------------------------------------------------------------------
// Fixture scaffolding (mirrors the production contract test's helpers:
// internal/refractor/ruleengine/full/capability_lens_contract_test.go).
// ---------------------------------------------------------------------------

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
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "spike-adj"}); err != nil {
		t.Fatalf("adj kv: %v", err)
	}
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "spike-core"}); err != nil {
		t.Fatalf("core kv: %v", err)
	}
	adjKV, err := conn.OpenKV(ctx, "spike-adj")
	if err != nil {
		t.Fatalf("open adj kv: %v", err)
	}
	coreKV, err := conn.OpenKV(ctx, "spike-core")
	if err != nil {
		t.Fatalf("open core kv: %v", err)
	}
	return adjKV, coreKV
}

// stableID returns a deterministic 20-char NanoID for a fixture name so vertex
// keys satisfy Contract #1 (ActorEnumerator validates NanoIDs).
func stableID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("spike:" + name) {
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
	body := map[string]any{"key": key, "class": typ, "data": data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal vertex: %v", err)
	}
	if _, err := kv.Put(context.Background(), key, raw); err != nil {
		t.Fatalf("put vertex %s: %v", key, err)
	}
	return key
}

// putEdge writes the two directional adjacency entries the production link
// bridge writes (evaluate.go:260) for a link "fromName <name> toName": an
// outbound entry under the source and an inbound entry under the target.
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

func mustBuild(t *testing.T, adjKV *substrate.KV, evt adjacency.CoreKVEvent) {
	t.Helper()
	if err := adjacency.Build(context.Background(), adjKV, evt); err != nil {
		t.Fatalf("adjacency build: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The fixture graph.
//
//	mgr (identity, manager)
//	rep (identity) -[reportsTo]-> mgr        (so mgr inherits rep's tasks)
//	bystander (identity)                     (the over-reprojection case)
//
//	taskDirect (task) -[assignedTo]-> mgr    WHERE expiresAt > now / status open
//	  taskDirect -[forOperation]-> op
//	  taskDirect -[scopedTo]-> tgt
//	taskRep (task) -[assignedTo]-> rep       (delegated to mgr via reportsTo)
//	  taskRep -[forOperation]-> op
//
// bystander shares the `op` vertex via a non-lens relation (watches), so the
// UNDIRECTED BFS from any task reaches bystander through op, but the directed
// compiled plan (assignedTo / reportsTo back to identity) never does.
//
// Sentence-correct link names (Contract #1, later-arriving vertex is source):
//	task assignedTo identity, task forOperation op, task scopedTo tgt,
//	report reportsTo identity, bystander watches op.
// ---------------------------------------------------------------------------

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
	// The over-reprojection edge: bystander shares op via a non-lens relation.
	putEdge(t, adjKV, "watches", "identity", "bystander", "op", "op1")

	return f
}

// ---------------------------------------------------------------------------
// Reproject-and-diff oracle (decision #3: REAL reproject-and-diff).
//
// reproject runs the production full engine evaluate of one lens for one actor
// against the current KV state, returning a stable string of the projected
// output (the RETURN row values). Diffing this string before/after a fixture
// mutation tells us whether the actor's projection ACTUALLY changed.
// ---------------------------------------------------------------------------

func reproject(t *testing.T, f *fixture, body, actorKey string) string {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(body)
	if err != nil {
		t.Fatalf("reproject parse: %v", err)
	}
	params := map[string]any{
		"actorKey": actorKey,
		"now":      float64(time.Now().Unix()),
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, f.adjKV, f.coreKV)
	if err != nil {
		t.Fatalf("reproject execute (%s): %v", actorKey, err)
	}
	// Canonicalize the projected rows to a stable comparable string.
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

// changedOutputActors reprojects the lens for every actor in the BFS superset
// before and after applying mutate, and returns the set of actors whose
// projected output actually changed.
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

// ---------------------------------------------------------------------------
// Helpers tying the BFS oracle and the compiled reverse-walk together.
// ---------------------------------------------------------------------------

func bfsActors(t *testing.T, f *fixture, eventKey, eventType string) map[string]struct{} {
	t.Helper()
	enum := pipeline.NewActorEnumerator(f.adjKV, f.coreKV, "identity")
	keys, err := enum.Enumerate(context.Background(), eventKey, eventType)
	if err != nil {
		t.Fatalf("BFS enumerate: %v", err)
	}
	return toSet(keys)
}

// compiledActors runs the verbatim reverse walk over EACH branch plan and unions
// the affected-anchor keys. Running per-branch is the spike's soundness fix: each
// branch is a contiguous chain, so the verbatim walkBackToAnchor is correct.
func compiledActors(t *testing.T, f *fixture, plans []*simple.QueryPlan, entry NodeEntry) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, plan := range plans {
		keys, err := reverseTraverse(context.Background(), plan, entry, f.adjKV)
		if err != nil {
			t.Fatalf("reverseTraverse: %v", err)
		}
		for k := range toSet(keys) {
			out[k] = struct{}{}
		}
	}
	return out
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

// ---------------------------------------------------------------------------
// The oracle assertions.
// ---------------------------------------------------------------------------

// event describes one CDC event the oracle exercises: a changed non-anchor node
// (for the compiled reverse-walk + the BFS oracle) and the link-bridge fan-out
// the production pipeline would perform for it.
type event struct {
	name      string
	nodeLabel string // label of the changed node (task / op / tgt) for the reverse walk
	nodeKey   string // its full vtx key
	// bfsSeeds are the vertex keys the production pipeline seeds the BFS from for
	// this event kind: a vertex event seeds from the vertex; a link event seeds
	// from BOTH endpoints (evaluate.go evaluateLinkFanOut); an aspect event seeds
	// from the parent vertex.
	bfsSeeds []seed
	// mutate applies the change to KV so the reproject-and-diff sees a real delta.
	mutate func(t *testing.T, f *fixture)
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

func TestInvalidationCompiler_OracleHolds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	lenses := []lensCase{
		{"myTasks", myTasksSpec},
		{"capabilityEphemeral", capabilityEphemeralSpec},
	}

	for _, lc := range lenses {
		lc := lc
		t.Run(lc.name, func(t *testing.T) {
			plans, err := CompilePlan(lc.body)
			if err != nil {
				t.Fatalf("CompilePlan(%s): %v", lc.name, err)
			}
			if len(plans) == 0 {
				t.Fatalf("%s: compiled forest has no branches", lc.name)
			}
			for bi, p := range plans {
				if len(p.Steps) == 0 {
					t.Fatalf("%s: branch %d has no steps", lc.name, bi)
				}
				t.Logf("%s branch %d: anchor=%s/%s steps=%s",
					lc.name, bi, p.AnchorLabel, p.AnchorVariable, renderSteps(p.Steps))
			}

			// Each lens gets a fresh fixture per event (mutations are destructive).
			strictSeen := false
			for _, mk := range eventMakers() {
				f := buildFixture(t)
				ev := mk(f)
				t.Run(ev.name, func(t *testing.T) {
					strict := runOracle(t, f, lc, plans, ev)
					if strict {
						strictSeen = true
					}
				})
			}
			if !strictSeen {
				t.Errorf("%s: no event demonstrated a STRICT subset (compiled ⊊ BFS); "+
					"the over-reprojection win was never exercised — fixture too simple", lc.name)
			}
		})
	}
}

// runOracle asserts AC #3 (a) subset, (b) no missed anchor, (c) records the win.
// Returns true if this event demonstrated a strict subset.
func runOracle(t *testing.T, f *fixture, lc lensCase, plans []*simple.QueryPlan, ev event) bool {
	t.Helper()

	// (a) compiled ⊆ BFS.
	bfs := unionBFS(t, f, ev.bfsSeeds)
	compiled := compiledActors(t, f, plans, NodeEntry{CoreKVKey: ev.nodeKey, NodeLabel: ev.nodeLabel})

	// Sanity: the compiled set must be non-empty where a hit is expected
	// (guards against a silent casing/direction mismatch faking a subset).
	if len(compiled) == 0 {
		t.Fatalf("[%s/%s] compiled set is EMPTY — direction/edge-name mismatch would fake a subset pass.\n"+
			"  branches=%d\n  changed=%s (%s)",
			lc.name, ev.name, len(plans), ev.nodeKey, ev.nodeLabel)
	}

	if !isSubset(compiled, bfs) {
		t.Fatalf("[%s/%s] AC#3(a) FAILED: compiled ⊄ BFS\n  compiled=%v\n  bfs=%v",
			lc.name, ev.name, sortedKeys(compiled), sortedKeys(bfs))
	}

	// (b) NO MISSED ANCHOR — real reproject-and-diff: actors whose projected
	// output actually changes must all be in the compiled set.
	bfsList := sortedKeys(bfs)
	changed := changedOutputActors(t, f, lc.body, bfsList, func() { ev.mutate(t, f) })
	if !isSubset(changed, compiled) {
		t.Fatalf("[%s/%s] AC#3(b) FAILED: MISSED ANCHOR — an actor whose output changed is NOT in the compiled set\n"+
			"  changed-output=%v\n  compiled=%v",
			lc.name, ev.name, sortedKeys(changed), sortedKeys(compiled))
	}

	// (c) record the win.
	win := len(bfs) - len(compiled)
	strict := win > 0
	t.Logf("[%s/%s] |BFS|=%d |compiled|=%d win(BFS−compiled)=%d strict=%v changed-output=%d",
		lc.name, ev.name, len(bfs), len(compiled), win, strict, len(changed))
	return strict
}

// eventMakers returns one constructor per CDC event kind (vertex, link, aspect).
// Each builds its event against a freshly-built fixture.
func eventMakers() []func(*fixture) event {
	return []func(*fixture) event{
		// VERTEX event: the taskDirect vertex changes. Pipeline seeds BFS from the
		// task vertex; the reverse walk starts from the task (ToLabel=="task").
		func(f *fixture) event {
			return event{
				name:      "vertex_task",
				nodeLabel: "task",
				nodeKey:   f.taskDirectKey,
				bfsSeeds:  []seed{{f.taskDirectKey, "task"}},
				mutate: func(t *testing.T, f *fixture) {
					// Flip the task closed so myTasks (status='open') and
					// capabilityEphemeral both drop it from the assignee's output.
					putVertexRaw(t, f.coreKV, f.taskDirectKey, "task",
						map[string]any{"status": "closed", "expiresAt": float64(1)})
				},
			}
		},
		// LINK event: the assignedTo link on taskRep changes. Pipeline seeds BFS
		// from BOTH endpoints (task + identity). Reverse walk starts from the task.
		func(f *fixture) event {
			return event{
				name:      "link_assignedTo",
				nodeLabel: "task",
				nodeKey:   f.taskRepKey,
				bfsSeeds:  []seed{{f.taskRepKey, "task"}, {f.repKey, "identity"}},
				mutate: func(t *testing.T, f *fixture) {
					// Remove the assignedTo edge: rep (and thus mgr via delegation)
					// lose the task. Tombstone both directional entries.
					removeEdge(t, f.adjKV, "assignedTo", "task", "rep", "identity", "rep")
				},
			}
		},
		// ASPECT event: the task.data aspect of taskDirect changes (expiresAt /
		// status). Pipeline seeds BFS from the parent task vertex. Reverse walk
		// starts from the task.
		func(f *fixture) event {
			return event{
				name:      "aspect_task_data",
				nodeLabel: "task",
				nodeKey:   f.taskDirectKey,
				bfsSeeds:  []seed{{f.taskDirectKey, "task"}},
				mutate: func(t *testing.T, f *fixture) {
					// Expire the grant in the past: capabilityEphemeral
					// (expiresAt > now) drops it; myTasks status flips closed.
					putVertexRaw(t, f.coreKV, f.taskDirectKey, "task",
						map[string]any{"status": "closed", "expiresAt": float64(1)})
				},
			}
		},
	}
}

func putVertexRaw(t *testing.T, kv *substrate.KV, key, typ string, data map[string]any) {
	t.Helper()
	body := map[string]any{"key": key, "class": typ, "data": data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal raw vertex: %v", err)
	}
	if _, err := kv.Put(context.Background(), key, raw); err != nil {
		t.Fatalf("put raw vertex: %v", err)
	}
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

func renderSteps(steps []simple.TraversalStep) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		dir := "?"
		switch s.Direction {
		case simple.Outbound:
			dir = "->"
		case simple.Inbound:
			dir = "<-"
		case simple.Both:
			dir = "--"
		}
		opt := ""
		if s.Optional {
			opt = " (opt)"
		}
		parts = append(parts, "("+s.FromLabel+")"+dir+"["+s.EdgeType+"]"+dir+"("+s.ToLabel+")"+opt)
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
