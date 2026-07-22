package edgemanifest

// Rule-engine proof of the staff workplace-spine lenses (edgeStaffWorkOrders
// and the work-order branch edgeManifestStaffReadGrants grants them), driven
// through the `full` engine — the one activation selects via engine:"full" —
// against an embedded NATS Core/Adjacency KV. Same harness shape as
// wellness-domain / clinic-domain's lens cypher tests.
//
// These exist because this package's other tests check only STRUCTURE (spec
// literals, adapter kinds, parse success), and the thing that actually needed
// proving here cannot be seen that way: edgeStaffWorkOrders walks
// `(work)<-[:containedIn*0..]-(place)` — a variable-length hop in the INBOUND
// direction, which no shipped lens in this repo had used before. Parsing says
// nothing about whether it enumerates children. The showcase topology is
// exactly the case that matters: the tech worksAt the BUILDING, the work order
// is at a UNIT inside it.
//
//	building A ←containedIn— unit A1 ←locatedAt— workorder "wo-unit"   (1 hop down)
//	building A ←locatedAt— workorder "wo-bldg"                          (0 hops)
//	building B ←locatedAt— workorder "wo-other"                         (not reachable)

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

func emCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-edgemanifest-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-edgemanifest-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-edgemanifest-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-edgemanifest-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// emNanoID returns a deterministic 20-char Contract #1 NanoID from a logical
// name (the wellness-domain helper's derivation).
func emNanoID(name string) string {
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

type emFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newEmFixture(t *testing.T) *emFixture {
	adjKV, coreKV := emCypherKVs(t)
	return &emFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *emFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := emNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *emFixture) key(name string) string {
	return "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
}

func (f *emFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

func (f *emFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs a personal-lens spec for one actor.
func (f *emFixture) project(t *testing.T, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "edge-manifest lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    actorKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// emStaffWorld builds the topology in this file's header comment and returns
// the staff actor's key.
func emStaffWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)
	f.vtx(t, "tech", "identity")
	f.vtx(t, "bldgA", "building")
	f.vtx(t, "bldgB", "building")
	f.vtx(t, "unitA1", "unit")
	f.vtx(t, "woUnit", "workorder")
	f.vtx(t, "woBldg", "workorder")
	f.vtx(t, "woOther", "workorder")

	f.aspect(t, "bldgA", "presentation", "locationPresentation", map[string]any{"name": "Riverside Building"})
	f.aspect(t, "unitA1", "presentation", "locationPresentation", map[string]any{"name": "Unit 1"})
	f.aspect(t, "woUnit", "report", "workOrderReport", map[string]any{
		"summary": "Basement riser valve is weeping", "priority": "urgent", "reportedAt": "2026-07-21T09:00:00Z"})
	f.aspect(t, "woBldg", "report", "workOrderReport", map[string]any{
		"summary": "Lobby door sticks", "priority": "normal", "reportedAt": "2026-07-21T10:00:00Z"})
	f.aspect(t, "woOther", "report", "workOrderReport", map[string]any{
		"summary": "Lift is out at B", "priority": "urgent", "reportedAt": "2026-07-21T11:00:00Z"})

	f.edge(t, "worksAt", "tech", "bldgA")
	f.edge(t, "containedIn", "unitA1", "bldgA")
	f.edge(t, "locatedAt", "woUnit", "unitA1")
	f.edge(t, "locatedAt", "woBldg", "bldgA")
	f.edge(t, "locatedAt", "woOther", "bldgB")
	return f
}

func emRowsByEntity(rows []ruleengine.ProjectionResult) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range rows {
		if id, _ := r.Values["entityId"].(string); id != "" {
			out[id] = r.Values
		}
	}
	return out
}

// TestEdgeStaffWorkOrders_WalksDownFromTheWorkplace is the load-bearing
// vector: the INBOUND variable-length hop must enumerate the workplace's
// children, or a tech who works at a building sees none of the work inside it
// — which is every work order in the showcase.
func TestEdgeStaffWorkOrders_WalksDownFromTheWorkplace(t *testing.T) {
	f := emStaffWorld(t)
	rows := emRowsByEntity(f.project(t, edgeStaffWorkOrdersSpec, f.key("tech")))

	unitRow, ok := rows[f.ids["woUnit"]]
	require.True(t, ok, "work order at a UNIT inside the workplace must project (the *0.. inbound hop)")
	require.Equal(t, "Basement riser valve is weeping", unitRow["summary"])
	require.Equal(t, "urgent", unitRow["priority"])
	require.Equal(t, "Unit 1", unitRow["placeName"], "the row names its own place, not the workplace")
	require.Equal(t, f.key("bldgA"), unitRow["workplaceKey"])
	require.Equal(t, "manifest.work", unitRow["ns"])
	require.Equal(t, "open", unitRow["status"])

	bldgRow, ok := rows[f.ids["woBldg"]]
	require.True(t, ok, "work order at the workplace ITSELF must project (the zero-hop case)")
	require.Equal(t, "Riverside Building", bldgRow["placeName"])

	_, leaked := rows[f.ids["woOther"]]
	require.False(t, leaked, "a work order at a building this actor does NOT work at must not project")
	require.Len(t, rows, 2)
}

// TestEdgeStaffWorkOrders_ResolvedStatusDerivesFromTheAspect pins that the
// mirror flips to resolved off the SAME `.resolution` marker ResolveWorkOrder
// writes — so a resolve that drains after a device reconnects needs no second
// write to model it.
func TestEdgeStaffWorkOrders_ResolvedStatusDerivesFromTheAspect(t *testing.T) {
	f := emStaffWorld(t)
	f.aspect(t, "woUnit", "resolution", "workOrderResolution", map[string]any{
		"notes": "Repacked the gland.", "resolvedAt": "2026-07-21T12:00:00Z",
		"resolvedBy": f.key("tech")})

	rows := emRowsByEntity(f.project(t, edgeStaffWorkOrdersSpec, f.key("tech")))
	require.Equal(t, "resolved", rows[f.ids["woUnit"]]["status"])
	require.Equal(t, "Repacked the gland.", rows[f.ids["woUnit"]]["resolutionNotes"])
	require.Equal(t, "open", rows[f.ids["woBldg"]]["status"], "an unresolved sibling stays open")
}

// TestEdgeStaffWorkOrders_NoWorkplaceProjectsNothing is the fail-closed half:
// the row set is derived from `worksAt`, so an actor without one has no
// workplace world at all. Unwire it and the rows go away with it.
func TestEdgeStaffWorkOrders_NoWorkplaceProjectsNothing(t *testing.T) {
	f := emStaffWorld(t)
	f.vtx(t, "resident", "identity")
	require.Empty(t, f.project(t, edgeStaffWorkOrdersSpec, f.key("resident")))
}

// TestStaffReadGrants_CoverTheWorkOrderAnchors is the same-commit grant
// discipline entity-browse F4 established, as a test rather than a promise: a
// manifest row whose anchor carries no read grant is silently dropped by
// Refractor's D1 gate, so the lens and its anchors ship together or the whole
// view is invisible for reasons nothing reports.
func TestStaffReadGrants_CoverTheWorkOrderAnchors(t *testing.T) {
	f := emStaffWorld(t)
	f.vtx(t, "maintRole", "role")
	f.edge(t, "holdsRole", "tech", "maintRole")

	rows := f.project(t, edgeManifestStaffReadGrantsSpec, f.key("tech"))
	require.Len(t, rows, 1)

	granted := map[string]bool{}
	anchors, _ := rows[0].Values["readableAnchors"].([]any)
	require.NotEmpty(t, anchors)
	for _, a := range anchors {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["anchorId"].(string); id != "" && m["anchorType"] == "workorder" {
			granted[id] = true
		}
	}
	require.True(t, granted[f.ids["woUnit"]], "the unit's work order must be readable")
	require.True(t, granted[f.ids["woBldg"]], "the building's own work order must be readable")
	require.False(t, granted[f.ids["woOther"]], "another building's work order must NOT be granted")
}
