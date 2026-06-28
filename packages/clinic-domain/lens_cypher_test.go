package clinicdomain

// Rule-engine proof of the clinic projection lenses (clinicAppointments,
// clinicProviders). These drive the lens specs through the `full` rule engine
// directly — the engine selected at activation via engine:"full" — against an
// embedded NATS Core/Adjacency KV, the same harness lease-signing /
// objects-base use for their lens cypher tests.
//
// What they prove that the unit/structure tests cannot:
//   - clinicAppointments is ONE ROW PER APPOINTMENT even with a patient + a
//     provider linked (0..1 × 0..1 = 1) — no fan-out, no output-key collision.
//   - the NEIGHBOUR aspect-hops resolve (p.demographics.data.fullName,
//     pr.profile.data.fullName/specialty off OPTIONAL-matched neighbours) — the
//     trickiest part of the spec, and the reason a no-WITH flat projection is
//     used (so the §4-B1 loftspace WITH-drop hazard does not apply).
//   - anchor aspect-hops (a.schedule.data.*, a.status.data.value) project.
//   - clinicProviders projects one row per NAMED provider (the WHERE presence
//     filter excludes a provider with no .profile).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func cypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-clinic-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-clinic-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-clinic-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-clinic-cypher-test")
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
	adjKV, coreKV *substrate.KV
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

// project runs a spec (no actor anchor — these are unanchored projections that
// seed-scan the graph, like loftspace's availableListings) and returns the rows.
func (f *lensFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "clinic lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func rowByKey(rows []ruleengine.ProjectionResult, key string) map[string]any {
	for _, r := range rows {
		if r.Values["key"] == key {
			return r.Values
		}
	}
	return nil
}

// TestClinicAppointments_JoinsPatientAndProvider proves the join: one row per
// appointment, with the neighbour aspect-hops (patientName / providerName /
// providerSpecialty) and anchor hops (startsAt / status) resolved.
func TestClinicAppointments_JoinsPatientAndProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	apptKey := f.vtx(t, "appt", "appointment")
	patientKey := f.vtx(t, "alice", "patient")
	providerKey := f.vtx(t, "drsam", "provider")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z", "reason": "Annual checkup"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	// The clinic-reminders .reminder aspect (sibling package) — clinicAppointments
	// surfaces its sentAt null-safely so the FE can show "reminder sent".
	f.aspect(t, "appt", "reminder", "appointmentReminder", map[string]any{"sentAt": "2026-06-30T15:00:00Z"})
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	rows := f.project(t, clinicAppointmentsSpec)
	require.Len(t, rows, 1, "exactly one row per appointment even with patient + provider joined")
	v := rows[0].Values
	require.Equal(t, apptKey, v["key"])
	require.Equal(t, apptKey, v["appointmentKey"])
	require.Equal(t, "2026-07-01T15:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-01T15:30:00Z", v["endsAt"])
	require.Equal(t, "Annual checkup", v["reason"])
	require.Equal(t, "scheduled", v["status"])
	require.Equal(t, patientKey, v["patientKey"])
	require.Equal(t, "Alice Rivera", v["patientName"], "neighbour aspect-hop p.demographics.data.fullName")
	require.Equal(t, providerKey, v["providerKey"])
	require.Equal(t, "Dr. Sam Okafor", v["providerName"], "neighbour aspect-hop pr.profile.data.fullName")
	require.Equal(t, "Cardiology", v["providerSpecialty"])
	require.Equal(t, "2026-06-30T15:00:00Z", v["reminderSentAt"], "null-safe .reminder hop surfaces the sent reminder")
}

// TestClinicAppointments_StatusTransitionProjects proves a confirmed appointment
// projects status=confirmed (the SetAppointmentStatus upsert path, at the lens
// level).
func TestClinicAppointments_StatusTransitionProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "confirmed"})
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	rows := f.project(t, clinicAppointmentsSpec)
	require.Len(t, rows, 1)
	require.Equal(t, "confirmed", rows[0].Values["status"])
	require.Nil(t, rows[0].Values["reason"], "absent optional reason → null column")
	require.Nil(t, rows[0].Values["reminderSentAt"], "no .reminder aspect → null reminderSentAt (null-safe)")
}

// TestClinicPatients_RostersNamedPatients proves the patient roster projects one
// row per NAMED patient (name only — no PHI), excluding a patient with no
// .demographics aspect (the WHERE presence filter), mirroring clinicProviders.
func TestClinicPatients_RostersNamedPatients(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	namedKey := f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{
		"fullName": "Alice Rivera", "dob": "1990-04-12T00:00:00Z", "email": "alice@example.com"})
	// A patient with NO .demographics aspect must be excluded by the WHERE filter.
	f.vtx(t, "ghost", "patient")

	rows := f.project(t, clinicPatientsSpec)
	require.Len(t, rows, 1, "only the named patient rosters; the demographics-less one is filtered out")
	v := rowByKey(rows, namedKey)
	require.NotNil(t, v)
	require.Equal(t, namedKey, v["patientKey"])
	require.Equal(t, "Alice Rivera", v["name"])
	// PHI must NOT be projected into the roster read model (Vault-plane deferred).
	_, hasDOB := v["dob"]
	require.False(t, hasDOB, "patient roster must not project DOB (PHI)")
	_, hasEmail := v["email"]
	require.False(t, hasEmail, "patient roster must not project email (PHI)")
}

// TestClinicProviders_RostersNamedProviders proves the roster projects one row
// per NAMED provider and excludes a provider with no .profile aspect (the WHERE
// presence filter).
func TestClinicProviders_RostersNamedProviders(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	namedKey := f.vtx(t, "drsam", "provider")
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD", "bio": "Heart specialist"})
	// A provider with NO .profile aspect must be excluded by the WHERE filter.
	f.vtx(t, "ghost", "provider")

	rows := f.project(t, clinicProvidersSpec)
	require.Len(t, rows, 1, "only the named provider rosters; the profile-less one is filtered out")
	v := rowByKey(rows, namedKey)
	require.NotNil(t, v)
	require.Equal(t, "Dr. Sam Okafor", v["name"])
	require.Equal(t, "Cardiology", v["specialty"])
	require.Equal(t, "MD", v["credentials"])
	// bio projects so the provider editor can seed it (read-modify-write).
	require.Equal(t, "Heart specialist", v["bio"])
	// A provider with no .timeOff aspect projects a null timeOff column.
	require.Nil(t, v["timeOff"], "timeOff is null when the provider has declared no blackouts")
	// Likewise no .hours aspect → a null hours column.
	require.Nil(t, v["hours"], "hours is null when the provider has set no availability windows")
}

// TestClinicProviders_ProjectsHoursWindows proves the non-scalar hours column:
// the provider's .hours aspect's `windows` array (a list of {day, openSec,
// closeSec} UTC seconds-of-day) projects verbatim into the read model, so the
// booking slot picker can compute the provider's open slots for a date.
func TestClinicProviders_ProjectsHoursWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	prKey := f.vtx(t, "drhour", "provider")
	f.aspect(t, "drhour", "profile", "providerProfile", map[string]any{"fullName": "Dr. Hours", "specialty": "Family"})
	f.aspect(t, "drhour", "hours", "providerHours", map[string]any{"windows": []any{
		map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200},
		map[string]any{"day": 3, "openSec": 32400, "closeSec": 61200},
	}})

	rows := f.project(t, clinicProvidersSpec)
	v := rowByKey(rows, prKey)
	require.NotNil(t, v)
	windows, ok := v["hours"].([]any)
	require.True(t, ok, "hours projects as an array, got %T", v["hours"])
	require.Len(t, windows, 2)
	first, ok := windows[0].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, first["day"])
	require.EqualValues(t, 32400, first["openSec"])
	require.EqualValues(t, 61200, first["closeSec"])
}

// TestClinicProviders_ProjectsTimeOffRanges proves the non-scalar timeOff column:
// the provider's .timeOff aspect's `ranges` array (a list of {from, to, reason})
// projects verbatim into the read model, so the time-off manager UI can
// read-modify-write the current blackouts.
func TestClinicProviders_ProjectsTimeOffRanges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	prKey := f.vtx(t, "drlee", "provider")
	f.aspect(t, "drlee", "profile", "providerProfile", map[string]any{"fullName": "Dr. Lee", "specialty": "Dermatology"})
	f.aspect(t, "drlee", "timeOff", "providerTimeOff", map[string]any{"ranges": []any{
		map[string]any{"from": "2026-07-01T00:00:00Z", "to": "2026-07-06T00:00:00Z", "reason": "Vacation"},
		map[string]any{"from": "2026-08-15T00:00:00Z", "to": "2026-08-16T00:00:00Z"},
	}})

	rows := f.project(t, clinicProvidersSpec)
	v := rowByKey(rows, prKey)
	require.NotNil(t, v)
	ranges, ok := v["timeOff"].([]any)
	require.True(t, ok, "timeOff projects as an array, got %T", v["timeOff"])
	require.Len(t, ranges, 2)
	first, ok := ranges[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "2026-07-01T00:00:00Z", first["from"])
	require.Equal(t, "2026-07-06T00:00:00Z", first["to"])
	require.Equal(t, "Vacation", first["reason"])
}
