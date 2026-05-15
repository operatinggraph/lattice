package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
)

// startEvalKVs spins up an in-memory NATS server and returns adjKV and coreKV buckets.
func startEvalKVs(t *testing.T) (adjKV, coreKV jetstream.KeyValue) {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
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

	ctx := context.Background()
	adjKV, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-test"})
	require.NoError(t, err)
	coreKV, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-test"})
	require.NoError(t, err)
	return adjKV, coreKV
}

// putCoreKV marshals props and stores them under key in kv.
func putCoreKV(t *testing.T, kv jetstream.KeyValue, key string, props map[string]any) {
	t.Helper()
	data, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, data)
	require.NoError(t, err)
}

// putAdjEdges marshals an AdjValue and stores it under adj.<nodeID>.
func putAdjEdges(t *testing.T, adjKV jetstream.KeyValue, nodeID string, edges []adjacency.EdgeEntry) {
	t.Helper()
	val := adjacency.AdjValue{Edges: edges}
	data, err := json.Marshal(val)
	require.NoError(t, err)
	_, err = adjKV.Put(context.Background(), "adj."+nodeID, data)
	require.NoError(t, err)
}

// mustCompile parses and compiles a query, requiring no error.
func mustCompile(t *testing.T, query string, keyFields []string) *QueryPlan {
	t.Helper()
	q, err := Parse(query)
	require.NoError(t, err)
	plan, err := Compile(q, keyFields)
	require.NoError(t, err)
	return plan
}

// TestEvaluate_AnchorNode_Upsert covers AC #1, #5, #7:
// anchor node update → forward traversal → Upsert result; unreferenced fields are silently ignored.
func TestEvaluate_AnchorNode_Upsert(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`,
		[]string{"agreement_id"},
	)

	// Populate Core KV.
	putCoreKV(t, coreKV, "node_agreement_a1", map[string]any{"id": "a1", "status": "active"})
	putCoreKV(t, coreKV, "node_identity_i1", map[string]any{"name": "Alice"})

	// Populate Adjacency KV: a1 has outbound HAS_PARTY edge to i1.
	putAdjEdges(t, adjKV, "node_agreement_a1", []adjacency.EdgeEntry{
		{EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", CoreKvKey: "edge_HAS_PARTY_e1", OtherNodeID: "node_identity_i1"},
	})

	entry := NodeEntry{
		CoreKVKey:  "node_agreement_a1",
		NodeLabel:  "agreement",
		IsDeleted:  false,
		Properties: map[string]any{"id": "a1", "status": "active"},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, results, 1)

	res := results[0]
	assert.False(t, res.Delete)
	assert.Equal(t, map[string]any{"agreement_id": "a1"}, res.Keys)
	assert.Equal(t, map[string]any{"agreement_id": "a1", "party_name": "Alice"}, res.Row)

	// "status" must not appear in Row (FR36a / AC #7).
	_, hasStatus := res.Row["status"]
	assert.False(t, hasStatus, "unreferenced field 'status' should not appear in the result row")
}

// TestEvaluate_AnchorNode_Delete covers AC #6:
// anchor node with isDeleted=true → Delete result; no traversal.
func TestEvaluate_AnchorNode_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`,
		[]string{"agreement_id"},
	)

	// Adjacency and identity node present but should not be touched for a delete.
	putAdjEdges(t, adjKV, "node_agreement_a1", []adjacency.EdgeEntry{
		{EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", CoreKvKey: "edge_HAS_PARTY_e1", OtherNodeID: "node_identity_i1"},
	})

	entry := NodeEntry{
		CoreKVKey:  "node_agreement_a1",
		NodeLabel:  "agreement",
		IsDeleted:  true,
		Properties: map[string]any{"id": "a1", "isDeleted": true},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, results, 1)

	res := results[0]
	assert.True(t, res.Delete)
	assert.Equal(t, map[string]any{"agreement_id": "a1"}, res.Keys)
	assert.Nil(t, res.Row)
}

// TestEvaluate_NonAnchorNode_ReverseTraversal covers AC #2:
// identity node changes → reverse traverse to anchor → re-evaluate → Upsert result.
func TestEvaluate_NonAnchorNode_ReverseTraversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`,
		[]string{"agreement_id"},
	)

	// Populate Core KV.
	putCoreKV(t, coreKV, "node_agreement_a1", map[string]any{"id": "a1"})
	putCoreKV(t, coreKV, "node_identity_i1", map[string]any{"name": "Bob"})

	// Adjacency KV: a1 has outbound HAS_PARTY → i1; i1 has inbound HAS_PARTY ← a1.
	putAdjEdges(t, adjKV, "node_agreement_a1", []adjacency.EdgeEntry{
		{EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", CoreKvKey: "edge_HAS_PARTY_e1", OtherNodeID: "node_identity_i1"},
	})
	putAdjEdges(t, adjKV, "node_identity_i1", []adjacency.EdgeEntry{
		{EdgeID: "e1", Name: "HAS_PARTY", Direction: "inbound", CoreKvKey: "edge_HAS_PARTY_e1", OtherNodeID: "node_agreement_a1"},
	})

	// Entry: the identity node changed (non-anchor).
	entry := NodeEntry{
		CoreKVKey:  "node_identity_i1",
		NodeLabel:  "identity",
		IsDeleted:  false,
		Properties: map[string]any{"name": "Bob"},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, results, 1)

	res := results[0]
	assert.False(t, res.Delete)
	assert.Equal(t, map[string]any{"agreement_id": "a1"}, res.Keys)
	assert.Equal(t, map[string]any{"agreement_id": "a1", "party_name": "Bob"}, res.Row)
}

// TestEvaluate_OptionalMatch_NoEdge covers AC #3:
// anchor node with no OPTIONAL MATCH edge → NULL values for optional variables.
func TestEvaluate_OptionalMatch_NoEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement) OPTIONAL MATCH (a)-[:HAS_CONTACT]->(c:contact) RETURN a.id AS agreement_id, c.email AS contact_email`,
		[]string{"agreement_id"},
	)

	putCoreKV(t, coreKV, "node_agreement_a1", map[string]any{"id": "a1"})
	// No HAS_CONTACT edge in adjKV.

	entry := NodeEntry{
		CoreKVKey:  "node_agreement_a1",
		NodeLabel:  "agreement",
		IsDeleted:  false,
		Properties: map[string]any{"id": "a1"},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, results, 1)

	res := results[0]
	assert.False(t, res.Delete)
	assert.Equal(t, map[string]any{"agreement_id": "a1"}, res.Keys)
	// contact_email must be nil (AC #3).
	assert.Contains(t, res.Row, "contact_email")
	assert.Nil(t, res.Row["contact_email"])
}

// TestEvaluate_AbsentProperty_Null covers AC #4:
// referenced property absent from the node's JSON → nil in output; no error.
func TestEvaluate_AbsentProperty_Null(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.legalName AS legal_name`,
		[]string{"agreement_id"},
	)

	putCoreKV(t, coreKV, "node_agreement_a1", map[string]any{"id": "a1"})
	// identity has no "legalName" field.
	putCoreKV(t, coreKV, "node_identity_i1", map[string]any{"name": "Alice"})

	putAdjEdges(t, adjKV, "node_agreement_a1", []adjacency.EdgeEntry{
		{EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", CoreKvKey: "edge_HAS_PARTY_e1", OtherNodeID: "node_identity_i1"},
	})

	entry := NodeEntry{
		CoreKVKey:  "node_agreement_a1",
		NodeLabel:  "agreement",
		IsDeleted:  false,
		Properties: map[string]any{"id": "a1"},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, results, 1)

	res := results[0]
	assert.False(t, res.Delete)
	assert.Nil(t, res.Row["legal_name"], "absent property must produce nil, not an error")
}

// TestEvaluate_RequiredMatch_NoEdge_Empty documents the skip-when-no-required-edge behavior:
// a required MATCH with no matching edge produces an empty result (not an error).
func TestEvaluate_RequiredMatch_NoEdge_Empty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startEvalKVs(t)
	ctx := context.Background()

	plan := mustCompile(t,
		`MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`,
		[]string{"agreement_id"},
	)

	// No HAS_PARTY edge for this anchor.
	putCoreKV(t, coreKV, "node_agreement_a1", map[string]any{"id": "a1"})

	entry := NodeEntry{
		CoreKVKey:  "node_agreement_a1",
		NodeLabel:  "agreement",
		IsDeleted:  false,
		Properties: map[string]any{"id": "a1"},
	}

	results, err := Evaluate(ctx, plan, entry, adjKV, coreKV)
	require.NoError(t, err)
	assert.Empty(t, results, "no result expected when required MATCH has no matching edge")
}
