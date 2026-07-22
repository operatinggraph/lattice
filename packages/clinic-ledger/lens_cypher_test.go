package clinicledger

// Rule-engine proof of the clinicNoShowSettlement convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness cafe-domain / semantic-contracts /
// lease-signing use.
//
//   - SCHEDULED: a scheduled (non-noShow) appointment never violates,
//     regardless of fee/account state.
//   - NOSHOW_NO_FEE: a noShow appointment with no noShowFeeCents (set before
//     this lens existed) never violates.
//   - NOSHOW_NO_ACCOUNT: noShow, carries a fee, the patient has no
//     clinic-ledger account yet — never violates (no missing_account gap;
//     this lens only converges once an account exists).
//   - NOSHOW_ACCOUNT_NO_CHARGE: noShow, carries a fee, account exists, no
//     clinictransaction settles this appointment yet — missing_charge true.
//   - NOSHOW_CHARGED: noShow, carries a fee, account exists, a
//     clinictransaction settles this appointment — converged.

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

func clLensKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-clled-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-clled-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-clled-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-clled-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func clNanoID(name string) string {
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

type clFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newClFixture(t *testing.T) *clFixture {
	adjKV, coreKV := clLensKVs(t)
	return &clFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *clFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := clNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *clFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *clFixture) edge(t *testing.T, name, fromName, toName string) {
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

// projectAt runs the anchored clinicNoShowSettlement spec for one appointment.
func (f *clFixture) projectAt(t *testing.T, apptName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(noShowSettlementSpec)
	require.NoError(t, err, "clinicNoShowSettlement cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    apptKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkAppointment seeds one appointment forPatient a fresh patient, with the
// given status and (optional, nil to omit) noShowFeeCents.
func (f *clFixture) mkAppointment(t *testing.T, name string, status string, feeCents any) {
	t.Helper()
	f.vtx(t, name, "appointment")
	statusData := map[string]any{"value": status}
	if feeCents != nil {
		statusData["noShowFeeCents"] = feeCents
	}
	f.aspect(t, name, "status", "appointmentStatus", statusData)
	f.vtx(t, name+"_patient", "patient")
	f.edge(t, "forPatient", name, name+"_patient")
}

func TestClinicNoShowSettlement_ScheduledNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "schedappt", "scheduled", nil)

	v := f.projectAt(t, "schedappt")[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["schedappt"], v["entityKey"])
	require.Equal(t, "scheduled", v["status"])
	require.Equal(t, false, v["missing_charge"], "not a noShow — never violates")
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowNoFee_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "nofeeappt", "noShow", nil)

	v := f.projectAt(t, "nofeeappt")[0].Values
	require.Equal(t, "noShow", v["status"])
	require.Nil(t, v["feeCents"], "no noShowFeeCents set")
	require.Equal(t, false, v["missing_charge"], "no fee to charge — never violates")
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowNoAccount_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "noacctappt", "noShow", 2500.0)

	v := f.projectAt(t, "noacctappt")[0].Values
	require.Nil(t, v["accountKey"], "patient has no clinic-ledger account yet")
	require.Equal(t, false, v["missing_charge"], "no account to charge yet — this gap doesn't gate (no missing_account gap)")
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowWithAccountNoCharge_MissingCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "unchargedappt", "noShow", 2500.0)
	f.aspect(t, "unchargedappt_patient", "ledgerAccount", "clinicLedgerAccountGuard", map[string]any{"accountKey": "vtx.clinicaccount.BBFAKEACCTHJKMNPQRST"})
	f.vtx(t, "unchargedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "unchargedappt_acct", "unchargedappt_patient")

	v := f.projectAt(t, "unchargedappt")[0].Values
	require.Equal(t, "vtx.clinicaccount."+f.ids["unchargedappt_acct"], v["accountKey"])
	require.Equal(t, 2500.0, v["feeCents"])
	require.Equal(t, true, v["missing_charge"], "no clinictransaction settles this appointment yet — violating")
	require.Equal(t, true, v["violating"])
}

func TestClinicNoShowSettlement_NoShowCharged_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "chargedappt", "noShow", 2500.0)
	f.vtx(t, "chargedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "chargedappt_acct", "chargedappt_patient")
	f.vtx(t, "chargedappt_tx", "clinictransaction")
	f.edge(t, "settles", "chargedappt_tx", "chargedappt")

	v := f.projectAt(t, "chargedappt")[0].Values
	require.Equal(t, false, v["missing_charge"], "a clinictransaction settles this appointment — converged")
	require.Equal(t, false, v["violating"])
}
