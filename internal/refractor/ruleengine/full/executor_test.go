package full

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/substrate"
)

// startExecKVs spins up an in-memory NATS server with adj and core KV buckets.
// These tests use Contract #1 vertex keys (vtx.<type>.<id>). The bridge is
// the executor's adjLookupID logic + EdgeEntry.OtherType extension.
//
// Each "logical" test name (alice, admin, room, ...) maps to a fixed
// valid 20-char NanoID; helpers materialize the corresponding
// vtx.<type>.<id> Core KV key when calling putVertex / putEdge.
func startExecKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
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
	t.Cleanup(func() { nc.Close(); s.Shutdown() })

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-exec-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-exec-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-exec-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-exec-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// fixtureRegistry tracks the (logicalName → Contract #1 vertex key)
// mapping produced by putVertex within a test. putEdge resolves logical
// names to vtx keys via this map so callers can author tests in plain
// english names while we exercise the executor's Contract #1 path.
type fixtureRegistry struct {
	byName   map[string]string // logicalName → vtx key
	idByName map[string]string // logicalName → bare NodeID
	typeByID map[string]string // bare NodeID → type segment
}

func newFixtureRegistry() *fixtureRegistry {
	return &fixtureRegistry{
		byName:   make(map[string]string),
		idByName: make(map[string]string),
		typeByID: make(map[string]string),
	}
}

// c1NanoID returns a deterministic 20-char Contract #1 NanoID derived
// from a logical test name. Uses the Lattice 58-char alphabet (no
// I/l/O/0). The hash is keyed on the name so the same name always
// resolves to the same NanoID across a test.
func c1NanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603 // FNV-64 offset
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

// putVertex writes a vertex to Core KV at vtx.<class>.<NanoID> where
// the NanoID is deterministically derived from name. Registers the
// mapping so putEdge can resolve `name` to a vtx key. Returns the
// full vtx key for callers that need to set up MATCH (i {key: ...}).
func putVertex(t *testing.T, reg *fixtureRegistry, kv *substrate.KV, name, class string, extra map[string]any) string {
	t.Helper()
	id := c1NanoID(name)
	vtxKey := "vtx." + class + "." + id
	reg.byName[name] = vtxKey
	reg.idByName[name] = id
	reg.typeByID[id] = class
	props := map[string]any{"key": vtxKey, "class": class}
	for k, v := range extra {
		props[k] = v
	}
	data, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), vtxKey, data)
	require.NoError(t, err)
	return vtxKey
}

// putEdge writes both inbound and outbound adjacency entries for one edge.
// fromName/toName must have been registered via putVertex.
func putEdge(t *testing.T, reg *fixtureRegistry, adjKV *substrate.KV, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID := reg.idByName[fromName]
	toID := reg.idByName[toName]
	require.NotEmpty(t, fromID, "fixture: %q not registered", fromName)
	require.NotEmpty(t, toID, "fixture: %q not registered", toName)
	fromType := reg.typeByID[fromID]
	toType := reg.typeByID[toID]
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID,
		EdgeID:    edgeID, Name: name,
		Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID,
		EdgeID:    edgeID, Name: name,
		Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
	}))
}

// vtxKey returns the registered Contract #1 vertex key for a logical name.
func vtxKey(reg *fixtureRegistry, name string) string {
	return reg.byName[name]
}

// parseExec compiles body and runs ExecuteWith with the given params.
func parseExec(t *testing.T, body string, ec ruleengine.EventContext, adjKV, coreKV *substrate.KV) []ruleengine.ProjectionResult {
	t.Helper()
	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err)
	out, err := eng.ExecuteWith(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err)
	return out
}

// --- per-feature executor tests ---

func TestExec_SimpleMatchReturn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	results := parseExec(t,
		`MATCH (i:identity {key: $k}) RETURN i.name AS name`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	require.Equal(t, "alice", results[0].Values["name"])
}

func TestExec_SoftDeleteFiltered(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice", "isDeleted": true})

	results := parseExec(t,
		`MATCH (i:identity {key: $k}) RETURN i.name AS name`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Empty(t, results)
}

func TestExec_MissingParameter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	eng := New()
	cr, err := eng.Parse(`MATCH (i:identity {key: $k}) RETURN i.name AS name`)
	require.NoError(t, err)
	_, err = eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.Error(t, err)
	var mpe *ruleengine.MissingParameterError
	require.True(t, errors.As(err, &mpe))
	require.Equal(t, "k", mpe.Name)
}

func TestExec_OutboundTraversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", map[string]any{"canonicalName": "admin"})
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")

	results := parseExec(t,
		`MATCH (i:identity {key: $k})-[:holdsRole]->(r:role) RETURN r.canonicalName AS role`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	require.Equal(t, "admin", results[0].Values["role"])
}

func TestExec_InboundTraversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "bob", "identity", nil)
	// bob reportsTo alice → from alice's perspective: alice <-[:reportsTo]- bob
	putEdge(t, reg, adjKV, "reportsTo", "bob", "alice")

	results := parseExec(t,
		`MATCH (i:identity {key: $k})<-[:reportsTo]-(r:identity) RETURN r.key AS reporter`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	require.Equal(t, vtxKey(reg, "bob"), results[0].Values["reporter"])
}

func TestExec_OptionalMatchNullPreserving(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	results := parseExec(t,
		`MATCH (i:identity {key: $k}) OPTIONAL MATCH (i)-[:holdsRole]->(r:role) RETURN i.name AS name, r.canonicalName AS role`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	require.Equal(t, "alice", results[0].Values["name"])
	require.Nil(t, results[0].Values["role"])
}

func TestExec_VarLengthTraversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "room", "location", nil)
	putVertex(t, reg, coreKV, "building", "location", nil)
	putEdge(t, reg, adjKV, "containedIn", "alice", "room")
	putEdge(t, reg, adjKV, "containedIn", "room", "building")

	results := parseExec(t,
		`MATCH (i:identity {key: $k})-[:containedIn*0..]->(l) RETURN l.key AS lkey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	// Hop 0 (alice itself), room, building → 3.
	require.Len(t, results, 3)
}

func TestExec_AntiPatternWhere(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "svc1", "service", nil)
	putEdge(t, reg, adjKV, "blocked", "alice", "svc1")

	// Returns identity only when NO blocked edge exists.
	results := parseExec(t,
		`MATCH (i:identity {key: $k}) WHERE NOT (i)-[:blocked]->(s:service) RETURN i.key AS k`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Empty(t, results)
}

func TestExec_WithCollectDistinct(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", map[string]any{"canonicalName": "admin"})
	putVertex(t, reg, coreKV, "viewer", "role", map[string]any{"canonicalName": "viewer"})
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	putEdge(t, reg, adjKV, "holdsRole", "alice", "viewer")

	results := parseExec(t,
		`MATCH (i:identity {key: $k})-[:holdsRole]->(r:role) RETURN i.key AS who, collect(DISTINCT r.canonicalName) AS roles`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	roles, ok := results[0].Values["roles"].([]any)
	require.True(t, ok)
	require.Len(t, roles, 2)
}

func TestExec_MapLiteralAndListConcat(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	results := parseExec(t,
		`MATCH (i:identity {key: $k}) RETURN {name: i.name, key: i.key} AS info, collect(i.key) + collect(i.name) AS combined`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	info, ok := results[0].Values["info"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "alice", info["name"])
	require.Equal(t, vtxKey(reg, "alice"), info["key"])
	combined, ok := results[0].Values["combined"].([]any)
	require.True(t, ok)
	require.Len(t, combined, 2)
}

func TestExec_PatternComprehension(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "svc1", "service", nil)
	putVertex(t, reg, coreKV, "opread", "operation", map[string]any{"operationType": "read"})
	putVertex(t, reg, coreKV, "opwrite", "operation", map[string]any{"operationType": "write"})
	putEdge(t, reg, adjKV, "permitsOperation", "svc1", "opread")
	putEdge(t, reg, adjKV, "permitsOperation", "svc1", "opwrite")

	results := parseExec(t,
		`MATCH (s:service {key: $k}) RETURN s.key AS skey, [(s)-[:permitsOperation]->(op) | {operationType: op.operationType}] AS ops`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "svc1")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	ops, ok := results[0].Values["ops"].([]any)
	require.True(t, ok)
	require.Len(t, ops, 2)
}
