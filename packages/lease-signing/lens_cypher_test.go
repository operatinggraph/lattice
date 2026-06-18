package leasesigning

// Rule-engine proof of the leaseApplicationComplete convergence cypher.
//
// These tests drive the lens spec (leaseApplicationCompleteSpec) through the
// `full` rule engine directly — the same engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV. They prove the
// cypher is ONE ROW PER ANCHOR (the §0.C guard would fail closed otherwise) and
// that the gap columns are strict bools that flip on a direct .outcome /
// .signature / .ssn write (AC #1 reprojection-on-linked-constituent + AC #4
// direct-write; the live bridge round-trip is 14.5).
//
// NOTE: these assert the engine PROJECTION (the rule-engine row). The bucket
// round-trip of the SCALAR convergence columns through the actorAggregate
// projection EnvelopeFn is the flagged Refractor gap (README "scalar convergence
// columns"; cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md E6) — the cypher itself
// is correct and proven here.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func cypherKVs(t *testing.T) (adjKV, coreKV jetstream.KeyValue) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	adjKV, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cypher-test"})
	require.NoError(t, err)
	coreKV, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cypher-test"})
	require.NoError(t, err)
	return adjKV, coreKV
}

// cNanoID returns a deterministic 20-char Contract #1 NanoID from a logical name.
func cNanoID(name string) string {
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

type lensFixture struct {
	adjKV, coreKV jetstream.KeyValue
	ids           map[string]string // logicalName -> bare NanoID
	types         map[string]string // bare NanoID -> type
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := cypherKVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := cNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
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

func (f *lensFixture) project(t *testing.T, appName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseApplicationCompleteSpec)
	require.NoError(t, err, "leaseApplicationComplete cypher must parse on the full engine")
	appKey := "vtx.leaseapp." + f.ids[appName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    appKey,
		"now":         time.Now().UTC().Format(time.RFC3339),
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestLeaseApplicationComplete_ProjectsOneRowPerAnchor (test 1 — AC #1 + §0.C).
// A multi-instance fixture (the applicant has 2 service instances) is the
// fan-out case the one-row-per-anchor guard would trip. The cypher MUST emit
// exactly one row carrying the right gap columns + non-null entityKey/applicant.
func TestLeaseApplicationComplete_ProjectsOneRowPerAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	appKey := f.vtx(t, "app", "leaseapp")
	idKey := f.vtx(t, "alice", "identity")
	// TWO service instances providedTo alice (one bgcheck, one payment), no
	// outcome yet — the multi-instance fan-out case.
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "exactly one weaver-targets row per leaseapp anchor even with 2 instances (§0.C guard)")
	v := rows[0].Values
	require.Equal(t, appKey, v["entityKey"], "entityKey must be the full leaseapp key")
	require.Equal(t, idKey, v["applicant"], "applicant must be the full identity key")
	require.Equal(t, true, v["missing_onboarding"])
	require.Equal(t, true, v["missing_bgcheck"])
	require.Equal(t, true, v["missing_payment"])
	require.Equal(t, true, v["missing_signature"])
	require.Equal(t, true, v["violating"])
	// the row key (actorKey, the first RETURN column) is the full leaseapp key;
	// the bare-NanoID convergence key is derived by BuildKey from it (14.2).
	require.Equal(t, appKey, v["actorKey"])
}

// TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite (test 2 — AC #1 + AC
// #4 + the dependent freshness). From all-gaps-open, recording the bgcheck
// instance's .outcome (status completed) flips missing_bgcheck false while a
// payment instance with NO outcome leaves missing_payment true; recording the
// applicant's ssn flips missing_onboarding false. Still exactly one row.
func TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"}) // onboarded
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z"})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"}) // no outcome
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"], "ssn present → onboarded")
	require.Equal(t, false, v["missing_bgcheck"], "completed bgcheck outcome → not missing")
	require.Equal(t, true, v["missing_payment"], "payment instance with no outcome → still missing")
	require.Equal(t, true, v["missing_signature"], "no signature yet")
	require.Equal(t, true, v["violating"], "payment + signature still open → violating")
}

// TestLeaseApplicationComplete_SignatureFlipsGap (test 8 — the assignTask gap
// closure at the lens level). Writing the leaseapp's .signature aspect flips
// missing_signature false; with all other gaps closed the row goes
// violating:false.
func TestLeaseApplicationComplete_SignatureFlipsGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtx(t, "bg1", "service")
	f.aspect(t, "bg1", "family", "family", map[string]any{"value": "backgroundCheck"})
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z"})
	f.vtx(t, "pay1", "service")
	f.aspect(t, "pay1", "family", "family", map[string]any{"value": "payment"})
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_signature"], "signature present → not missing")
	require.Equal(t, false, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"])
	require.Equal(t, false, v["missing_payment"])
	require.Equal(t, false, v["violating"], "all gaps closed → not violating")
}
