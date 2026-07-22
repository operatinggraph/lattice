package wellnessdomain

// Rule-engine proof of the wellness projection lenses (wellnessStudios,
// wellnessSessions, wellnessBookings). These drive the lens specs through the
// `full` rule engine directly — the engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV, the same
// harness clinic-domain / lease-signing use for their lens cypher tests.
//
// What they prove that the unit/structure tests cannot:
//   - wellnessSessions is ONE ROW PER SESSION even with a studio linked
//     (0..1 = 1) — no fan-out, no output-key collision.
//   - the NEIGHBOUR aspect-hop resolves (s.profile.data.name off the
//     OPTIONAL-matched studio).
//   - wellnessBookings joins BOTH session and booker neighbours in one flat
//     row.
//   - wellnessStudios / a WHERE presence filter excludes a studio with no
//     .profile.

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

func wdCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-wellnessdom-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-wellnessdom-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-wellnessdom-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-wellnessdom-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// wdNanoID returns a deterministic 20-char Contract #1 NanoID from a logical name.
func wdNanoID(name string) string {
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

type wdFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newWdFixture(t *testing.T) *wdFixture {
	adjKV, coreKV := wdCypherKVs(t)
	return &wdFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *wdFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := wdNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *wdFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *wdFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs a spec (no actor anchor — unanchored projections that
// seed-scan the graph, mirroring clinic-domain's lensFixture.project) and
// returns the rows.
func (f *wdFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "wellness lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func wdRowByKey(rows []ruleengine.ProjectionResult, key string) map[string]any {
	for _, r := range rows {
		if r.Values["key"] == key {
			return r.Values
		}
	}
	return nil
}

// TestWellnessStudios_RostersNamedStudios proves the studio picker projects
// one row per NAMED studio, excluding a studio with no .profile aspect (the
// WHERE presence filter), mirroring clinicProviders.
func TestWellnessStudios_RostersNamedStudios(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	namedKey := f.vtx(t, "sunrise", "studio")
	f.aspect(t, "sunrise", "profile", "studioProfile", map[string]any{"name": "Sunrise Yoga Room"})
	// A studio with NO .profile aspect must be excluded by the WHERE filter.
	f.vtx(t, "ghost", "studio")

	rows := f.project(t, wellnessStudiosSpec)
	require.Len(t, rows, 1, "only the named studio rosters; the profile-less one is filtered out")
	v := wdRowByKey(rows, namedKey)
	require.NotNil(t, v)
	require.Equal(t, namedKey, v["studioKey"])
	require.Equal(t, "Sunrise Yoga Room", v["name"])
}

// TestWellnessSessions_JoinsStudio proves the schedule-grid join: one row per
// session, with the neighbour aspect-hop (studioName) and anchor hops
// (startsAt/endsAt/capacity) resolved.
func TestWellnessSessions_JoinsStudio(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	sessKey := f.vtx(t, "flow", "session")
	studioKey := f.vtx(t, "sunrise", "studio")
	f.aspect(t, "sunrise", "profile", "studioProfile", map[string]any{"name": "Sunrise Yoga Room"})
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1, "exactly one row per session even with studio joined")
	v := rows[0].Values
	require.Equal(t, sessKey, v["key"])
	require.Equal(t, sessKey, v["sessionKey"])
	require.Equal(t, "Vinyasa Flow", v["name"])
	require.Equal(t, "2026-07-08T09:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-08T09:30:00Z", v["endsAt"])
	require.Equal(t, 20.0, v["capacity"])
	require.Equal(t, studioKey, v["studioKey"])
	require.Equal(t, "Sunrise Yoga Room", v["studioName"], "neighbour aspect-hop s.profile.data.name")
}

// TestWellnessSessions_NoStudioNullSafe proves a session with no resolvable
// studio link still projects one row, with null studio columns (null-safe by
// key-shape, the clinicAppointments OPTIONAL idiom).
func TestWellnessSessions_NoStudioNullSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "orphan", "session")
	f.aspect(t, "orphan", "schedule", "sessionSchedule", map[string]any{
		"name": "Orphan Class", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 5.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["studioKey"])
	require.Nil(t, rows[0].Values["studioName"])
}

// TestWellnessBookings_JoinsSessionAndBooker proves the roster / my-classes
// join: one row per booking, with both the session neighbour (sessionName,
// startsAt/endsAt) and booker neighbour (bookerKey) resolved.
func TestWellnessBookings_JoinsSessionAndBooker(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	bookingKey := f.vtx(t, "booking1", "booking")
	sessKey := f.vtx(t, "flow", "session")
	bookerKey := f.vtx(t, "alice", "identity")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.aspect(t, "booking1", "status", "bookingStatus", map[string]any{"value": "booked", "rate": "resident", "seat": 1.0})
	f.edge(t, "forSession", "booking1", "flow")
	f.edge(t, "bookedBy", "booking1", "alice")

	rows := f.project(t, wellnessBookingsSpec)
	require.Len(t, rows, 1, "exactly one row per booking even with session + booker joined")
	v := rows[0].Values
	require.Equal(t, bookingKey, v["key"])
	require.Equal(t, bookingKey, v["bookingKey"])
	require.Equal(t, "booked", v["status"])
	require.Equal(t, "resident", v["rate"])
	require.Equal(t, sessKey, v["sessionKey"])
	require.Equal(t, "Vinyasa Flow", v["sessionName"], "neighbour aspect-hop se.schedule.data.name")
	require.Equal(t, "2026-07-08T09:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-08T09:30:00Z", v["endsAt"])
	require.Equal(t, bookerKey, v["bookerKey"])
}
