package frontdesk

// Rule-engine proof of the frontDeskBookings lens, driven through the `full`
// engine (engine:"full") against an embedded NATS Core/Adjacency KV — the
// same harness one-bill's lens test uses (packages/one-bill/lens_cypher_test.go).
// Unanchored whole-graph scan (no Parameters needed, same as one-bill).
//
//   - TestFrontDeskBookings_ProjectsResidentRateRow: a booked, resident-rate
//     booking (residentRate link present) projects one row keyed to its
//     leaseapp, carrying the session name/start time.
//   - TestFrontDeskBookings_SkipsStandardRateBooking: a booked booking with
//     NO residentRate link (standard rate — no lease claimed, or claim
//     didn't match) projects nothing; front-desk shows only a resident's
//     OWN booking, never every booking in the building.
//   - TestFrontDeskBookings_SkipsCancelledBooking: a cancelled booking —
//     CancelBooking soft-deletes the booking vertex (isDeleted:true), it
//     never rewrites .status.value (bookingStatusAspectDDL's enum is
//     ["booked"] only) — is absent from the result, proving the engine's
//     standard isDeleted filter (executor.go) covers this lens same as
//     every other.
//
// The Inc 5 clinic tail mirrors this same shape for visitsSpec:
// TestFrontDeskVisits_ProjectsResidentVisitRow /
// _SkipsAppointmentWithNoResidentVisitLink / _SkipsNonScheduledAppointment /
// _SkipsCancelledAppointment.

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

func fdCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-frontdesk-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-frontdesk-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-frontdesk-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-frontdesk-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func fdNanoID(name string) string {
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

type fdFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newFdFixture(t *testing.T) *fdFixture {
	adjKV, coreKV := fdCypherKVs(t)
	return &fdFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *fdFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	return f.vtxDeleted(t, name, typ, false)
}

func (f *fdFixture) vtxDeleted(t *testing.T, name, typ string, deleted bool) string {
	t.Helper()
	id := fdNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": deleted, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *fdFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *fdFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs the given spec unanchored (no Parameters — a whole-graph
// scan, same as one-bill's lenses).
func (f *fdFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkBooking seeds a booking with a .status aspect, optionally linked to a
// session (forSession) and/or a leaseapp (residentRate).
func (f *fdFixture) mkBooking(t *testing.T, name, status, rate string, withSession, withResidentRate bool) {
	t.Helper()
	f.mkBookingDeleted(t, name, status, rate, withSession, withResidentRate, false)
}

func (f *fdFixture) mkBookingDeleted(t *testing.T, name, status, rate string, withSession, withResidentRate, deleted bool) {
	t.Helper()
	f.vtxDeleted(t, name, "booking", deleted)
	f.aspect(t, name, "status", "bookingStatus", map[string]any{"value": status, "rate": rate, "seat": 1})
	if withSession {
		f.vtx(t, name+"_session", "session")
		f.aspect(t, name+"_session", "schedule", "sessionSchedule", map[string]any{
			"name": "Sat mobility class", "startsAt": "2026-07-11T09:00:00Z", "endsAt": "2026-07-11T09:45:00Z", "capacity": 10.0,
		})
		f.edge(t, "forSession", name, name+"_session")
	}
	if withResidentRate {
		f.vtx(t, name+"_lease", "leaseapp")
		f.edge(t, "residentRate", name, name+"_lease")
	}
}

func TestFrontDeskBookings_ProjectsResidentRateRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkBooking(t, "b1", "booked", "resident", true, true)

	rows := f.project(t, bookingsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.booking."+f.ids["b1"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["b1_lease"], v["leaseAppKey"])
	require.Equal(t, "vtx.session."+f.ids["b1_session"], v["sessionKey"])
	require.Equal(t, "Sat mobility class", v["sessionName"])
	require.Equal(t, "2026-07-11T09:00:00Z", v["startsAt"])
	require.Equal(t, "wellness", v["source"])
}

func TestFrontDeskBookings_SkipsStandardRateBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkBooking(t, "b1", "booked", "standard", true, false)

	rows := f.project(t, bookingsSpec)
	require.Empty(t, rows, "a booking with no residentRate link must not project — front-desk shows only a resident's own booking")
}

func TestFrontDeskBookings_SkipsCancelledBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkBookingDeleted(t, "b1", "booked", "resident", true, true, true)

	rows := f.project(t, bookingsSpec)
	require.Empty(t, rows, "a soft-deleted (cancelled) booking must be filtered by the engine's isDeleted guard")
}

// mkUnit seeds a unit vertex with .listing + .address aspects
// (loftspace-domain's shape).
func (f *fdFixture) mkUnit(t *testing.T, name string, rentAmount float64, rentCurrency string, leaseTermMonths float64, line1 string) {
	t.Helper()
	f.vtx(t, name, "unit")
	f.aspect(t, name, "listing", "loftspaceListing", map[string]any{
		"rentAmount": rentAmount, "rentCurrency": rentCurrency, "bedrooms": 1.0, "leaseTermMonths": leaseTermMonths, "status": "leased",
	})
	f.aspect(t, name, "address", "loftspaceAddress", map[string]any{"line1": line1, "city": "Metropolis"})
}

func TestFrontDeskLeaseDetails_ProjectsUnitRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.vtx(t, "l1", "leaseapp")
	f.mkUnit(t, "l1_unit", 2500, "USD", 12, "123 Main St")
	f.edge(t, "appliesToUnit", "l1", "l1_unit")

	rows := f.project(t, leaseDetailsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["l1"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["l1"], v["leaseAppKey"])
	require.Equal(t, "vtx.unit."+f.ids["l1_unit"], v["unitKey"])
	require.Equal(t, "123 Main St", v["unitAddress"])
	require.Equal(t, 2500.0, v["unitRent"])
	require.Equal(t, "USD", v["unitCurrency"])
	require.Equal(t, 12.0, v["unitLeaseTermMonths"])
}

func TestFrontDeskLeaseDetails_NullsWhenNoUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.vtx(t, "l1", "leaseapp")

	rows := f.project(t, leaseDetailsSpec)
	require.Len(t, rows, 1, "a leaseapp with no appliesToUnit link still projects a row, rent/term null")
	v := rows[0].Values
	require.Nil(t, v["unitKey"])
	require.Nil(t, v["unitRent"])
}

// mkAppointment seeds an appointment with a .schedule + .status aspect,
// optionally linked to a leaseapp (residentVisit) — mirrors mkBooking's
// shape for the clinic side of the unified context (Inc 5).
func (f *fdFixture) mkAppointment(t *testing.T, name, status string, withResidentVisit bool) {
	t.Helper()
	f.mkAppointmentDeleted(t, name, status, withResidentVisit, false)
}

func (f *fdFixture) mkAppointmentDeleted(t *testing.T, name, status string, withResidentVisit, deleted bool) {
	t.Helper()
	f.vtxDeleted(t, name, "appointment", deleted)
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": "2026-07-11T15:00:00Z", "endsAt": "2026-07-11T15:30:00Z", "remindAt": "2026-07-10T15:00:00Z", "reason": "Annual checkup",
	})
	if withResidentVisit {
		f.vtx(t, name+"_lease", "leaseapp")
		f.edge(t, "residentVisit", name, name+"_lease")
	}
}

func TestFrontDeskVisits_ProjectsResidentVisitRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkAppointment(t, "a1", "scheduled", true)

	rows := f.project(t, visitsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["a1"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["a1_lease"], v["leaseAppKey"])
	require.Equal(t, "2026-07-11T15:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-11T15:30:00Z", v["endsAt"])
	require.Nil(t, v["reason"], "the visit reason must never project — front desk sees existence + time only")
}

func TestFrontDeskVisits_SkipsAppointmentWithNoResidentVisitLink(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkAppointment(t, "a1", "scheduled", false)

	rows := f.project(t, visitsSpec)
	require.Empty(t, rows, "an appointment with no residentVisit link must not project — front-desk shows only a resident's own visit")
}

func TestFrontDeskVisits_SkipsNonScheduledAppointment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkAppointment(t, "a1", "completed", true)

	rows := f.project(t, visitsSpec)
	require.Empty(t, rows, "a completed/cancelled/noShow appointment must not project — front-desk only badges an upcoming visit")
}

func TestFrontDeskVisits_SkipsCancelledAppointment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newFdFixture(t)
	f.mkAppointmentDeleted(t, "a1", "scheduled", true, true)

	rows := f.project(t, visitsSpec)
	require.Empty(t, rows, "a soft-deleted appointment must be filtered by the engine's isDeleted guard")
}
