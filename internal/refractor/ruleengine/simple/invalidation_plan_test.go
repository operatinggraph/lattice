package simple

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/substrate"
)

const myTasksSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
RETURN identity.key AS actorKey,
  collect(DISTINCT {taskKey: task.key}) AS openTasks
`

const capabilityEphemeralSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)<-[:assignedTo]-(task2:task)
  WHERE task2.data.expiresAt > $now
OPTIONAL MATCH (task2)-[:forOperation]->(op2)
OPTIONAL MATCH (task2)-[:scopedTo]->(tgt2)
RETURN identity.key AS actorKey,
  collect(DISTINCT {taskKey: task.key}) + collect(DISTINCT {taskKey: task2.key}) AS ephemeralGrants
`

type stepShape struct {
	from string
	edge string
	dir  EdgeDirection
	to   string
}

func shapeOf(steps []TraversalStep) []stepShape {
	out := make([]stepShape, len(steps))
	for i, s := range steps {
		out[i] = stepShape{from: s.FromLabel, edge: s.EdgeType, dir: s.Direction, to: s.ToLabel}
	}
	return out
}

func TestCompileInvalidationForest_MyTasksShape(t *testing.T) {
	forest, err := CompileInvalidationForest(myTasksSpec)
	if err != nil {
		t.Fatalf("compile myTasks: %v", err)
	}
	if forest.AnchorLabel != "identity" || forest.AnchorVariable != "identity" {
		t.Fatalf("anchor mismatch: %+v", forest)
	}
	if len(forest.Branches) != 2 {
		t.Fatalf("myTasks: expected 2 branches, got %d", len(forest.Branches))
	}
	// branch 0: (identity)<-[assignedTo]-(task), (task)->[forOperation]->(op)
	// The leaf nodes (op)/(tgt) carry no inline label in the cypher, so their
	// ToLabel stays "" (no introducing binding to backfill from).
	want0 := []stepShape{
		{"identity", "assignedTo", Inbound, "task"},
		{"task", "forOperation", Outbound, ""},
	}
	// branch 1: (identity)<-[assignedTo]-(task), (task)->[scopedTo]->(tgt)
	want1 := []stepShape{
		{"identity", "assignedTo", Inbound, "task"},
		{"task", "scopedTo", Outbound, ""},
	}
	assertBranchSet(t, forest.Branches, [][]stepShape{want0, want1})
}

func TestCompileInvalidationForest_CapabilityEphemeralShape(t *testing.T) {
	forest, err := CompileInvalidationForest(capabilityEphemeralSpec)
	if err != nil {
		t.Fatalf("compile capabilityEphemeral: %v", err)
	}
	if len(forest.Branches) != 4 {
		t.Fatalf("capabilityEphemeral: expected 4 branches, got %d", len(forest.Branches))
	}
	// Leaf nodes (op/op2/tgt/tgt2) are unlabeled in the cypher → ToLabel "".
	// The re-referenced (report:identity) delegation hop is label-backfilled to
	// "identity" on BOTH ends (AC3c).
	wants := [][]stepShape{
		{{"identity", "assignedTo", Inbound, "task"}, {"task", "forOperation", Outbound, ""}},
		{{"identity", "assignedTo", Inbound, "task"}, {"task", "scopedTo", Outbound, ""}},
		{{"identity", "reportsTo", Inbound, "identity"}, {"identity", "assignedTo", Inbound, "task"}, {"task", "forOperation", Outbound, ""}},
		{{"identity", "reportsTo", Inbound, "identity"}, {"identity", "assignedTo", Inbound, "task"}, {"task", "scopedTo", Outbound, ""}},
	}
	assertBranchSet(t, forest.Branches, wants)
}

func TestCompileInvalidationForest_AnchorOnlyCompiles(t *testing.T) {
	// An anchor-only lens (bound anchor, no relationships) is sound: only the
	// anchor's own change matters (handled by Execution). It must compile to a
	// zero-branch forest, not error with "no traversal branch reaches the anchor".
	body := `
MATCH (identity:identity {key: $actorKey})
RETURN identity.key AS actorKey, identity.data.name AS name
`
	forest, err := CompileInvalidationForest(body)
	if err != nil {
		t.Fatalf("anchor-only compile errored: %v", err)
	}
	if forest.AnchorLabel != "identity" || forest.AnchorVariable != "identity" {
		t.Fatalf("anchor mismatch: %+v", forest)
	}
	if len(forest.Branches) != 0 {
		t.Fatalf("anchor-only: expected 0 branches, got %d", len(forest.Branches))
	}
}

// assertBranchSet asserts the forest's branch shapes equal the expected set
// (order-independent).
func assertBranchSet(t *testing.T, branches []*QueryPlan, wants [][]stepShape) {
	t.Helper()
	if len(branches) != len(wants) {
		t.Fatalf("branch count: got %d want %d", len(branches), len(wants))
	}
	matched := make([]bool, len(wants))
	for _, b := range branches {
		got := shapeOf(b.Steps)
		found := false
		for wi, w := range wants {
			if matched[wi] {
				continue
			}
			if shapeEq(got, w) {
				matched[wi] = true
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("branch %+v matched no expected shape", got)
		}
	}
}

func shapeEq(a, b []stepShape) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestForestNotMissedAnchor_VsFlatPlan proves the central spike finding: a flat
// (non-forest) plan reverse-walks a delegation-branch leaf back through the
// direct branch and DROPS the manager anchor — a missed revocation. The forest
// compiler does not. This guards against regression to the unsound flat design.
func TestForestNotMissedAnchor_VsFlatPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, _ := startTestKVs(t)

	mgrID := tID("identity:mgr")
	repID := tID("identity:rep")
	taskRepID := tID("task:rep")

	// rep reportsTo mgr; taskRep assignedTo rep. So changing taskRep must
	// invalidate BOTH rep (direct) and mgr (delegation).
	putTestEdge(t, adjKV, "reportsTo", "identity", repID, "identity", mgrID)
	putTestEdge(t, adjKV, "assignedTo", "task", taskRepID, "identity", repID)

	entry := NodeEntry{CoreKVKey: "vtx.task." + taskRepID, NodeLabel: "task"}

	// The forest compiler: both anchors present.
	forest, err := CompileInvalidationForest(capabilityEphemeralSpec)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := forest.AffectedAnchors(context.Background(), entry, adjKV)
	if err != nil {
		t.Fatalf("AffectedAnchors: %v", err)
	}
	gotSet := map[string]struct{}{}
	for _, k := range got {
		gotSet[k] = struct{}{}
	}
	mgrKey := "vtx.identity." + mgrID
	repKey := "vtx.identity." + repID
	if _, ok := gotSet[mgrKey]; !ok {
		t.Fatalf("forest MISSED the manager anchor %s; got %v", mgrKey, got)
	}
	if _, ok := gotSet[repKey]; !ok {
		t.Fatalf("forest missed the direct anchor %s; got %v", repKey, got)
	}

	// A FLAT plan (all steps in one Steps slice) reverse-walks the delegation
	// leaf back through the direct branch and drops mgr — demonstrate the bug
	// the forest avoids.
	flat := flattenForest(forest)
	flatKeys, err := reverseTraverse(context.Background(), flat, entry, adjKV)
	if err != nil {
		t.Fatalf("flat reverseTraverse: %v", err)
	}
	flatSet := map[string]struct{}{}
	for _, k := range flatKeys {
		flatSet[k] = struct{}{}
	}
	if _, ok := flatSet[mgrKey]; ok {
		t.Fatalf("flat plan unexpectedly found mgr — fixture does not demonstrate the unsoundness")
	}
}

// flattenForest concatenates all branch steps into one Steps slice — the
// UNSOUND flat design the forest replaces. Used only to demonstrate the bug.
func flattenForest(forest *InvalidationForest) *QueryPlan {
	var steps []TraversalStep
	for _, b := range forest.Branches {
		steps = append(steps, b.Steps...)
	}
	return &QueryPlan{
		AnchorLabel:    forest.AnchorLabel,
		AnchorVariable: forest.AnchorVariable,
		Steps:          steps,
	}
}

// --- minimal NATS fixture for this package's reverse-walk test ---

func startTestKVs(t *testing.T) (*substrate.KV, *substrate.KV) {
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
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "inv-adj"}); err != nil {
		t.Fatalf("adj kv: %v", err)
	}
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "inv-core"}); err != nil {
		t.Fatalf("core kv: %v", err)
	}
	adjKV, err := conn.OpenKV(ctx, "inv-adj")
	if err != nil {
		t.Fatalf("open adj kv: %v", err)
	}
	coreKV, err := conn.OpenKV(ctx, "inv-core")
	if err != nil {
		t.Fatalf("open core kv: %v", err)
	}
	return adjKV, coreKV
}

func tID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("inv:" + name) {
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

func putTestEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromID, toType, toID string) {
	t.Helper()
	edgeID := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	for _, e := range []adjacency.CoreKVEvent{
		{CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType},
		{CoreKvKey: edgeID, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType},
	} {
		if err := adjacency.Build(context.Background(), adjKV, e); err != nil {
			t.Fatalf("build edge: %v", err)
		}
	}
}
