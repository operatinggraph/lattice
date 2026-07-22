package semanticcontracts

// Rule-engine proof of the clauseSatisfaction convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness lease-signing / clinic-reminders /
// objects-base use.
//
//   - UNCHARGED: no transaction authorizedBy the clause — violating,
//     missing_charge true.
//   - CHARGED: a transaction authorizedBy the clause exists — not violating,
//     missing_charge false (converged; the row lingers non-violating per
//     the design's R3 v1 constraint, never deleted).
//   - one row per anchor even with the chargesTo account linked.
//   - CONDITIONED: a live conditionedOn link gates the charge; an absent one
//     (never conditioned, or the target vertex tombstoned) suppresses it.
//   - JUDGMENT: an assigned inspector with no .inspection aspect yet is
//     violating (missing_inspection); recording the inspection converges it.

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

func bcCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-bespcon-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-bespcon-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-bespcon-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-bespcon-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func bcNanoID(name string) string {
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

type bcFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newBcFixture(t *testing.T) *bcFixture {
	adjKV, coreKV := bcCypherKVs(t)
	return &bcFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *bcFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := bcNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *bcFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *bcFixture) edge(t *testing.T, name, fromName, toName string) {
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

// projectAt runs the anchored clauseSatisfaction spec for one clause.
func (f *bcFixture) projectAt(t *testing.T, clauseName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(clauseSatisfactionSpec)
	require.NoError(t, err, "clauseSatisfaction cypher must parse on the full engine")
	clauseKey := "vtx.clause." + f.ids[clauseName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    clauseKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkClause seeds one unconditioned computational clause with .terms{amountCents}
// + .status{active}, linked to a charged account.
func (f *bcFixture) mkClause(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "clause")
	f.aspect(t, name, "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": amountCents, "period": "oneTime"})
	f.aspect(t, name, "status", "clauseStatus", map[string]any{"state": "active"})
	f.vtx(t, name+"_acct", "account")
	f.edge(t, "chargesTo", name, name+"_acct")
}

// TestClauseSatisfaction_Uncharged — no authorizedBy transaction yet: violating,
// missing_charge true, amountCents/accountKey project through.
func TestClauseSatisfaction_Uncharged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkClause(t, "clause1", 4500)

	rows := f.projectAt(t, "clause1")
	require.Len(t, rows, 1, "exactly one row per clause anchor")
	v := rows[0].Values
	require.Equal(t, "vtx.clause."+f.ids["clause1"], v["entityKey"])
	require.Equal(t, "vtx.clause."+f.ids["clause1"], v["clauseKey"])
	require.Equal(t, "vtx.account."+f.ids["clause1_acct"], v["accountKey"])
	require.Equal(t, 4500.0, v["amountCents"])
	require.Equal(t, true, v["missing_charge"], "no authorizedBy transaction yet — violating")
	require.Equal(t, true, v["violating"])
}

// TestClauseSatisfaction_LegacyNoConditionedField — a pre-Fire-V2 clause
// whose .terms aspect has no `conditioned` key at all (Fire V1's exact
// shape): missing_charge must still gate purely on chargeCount, the same as
// an explicitly-unconditioned (conditioned:false) clause. Regression test for
// a real bug this fire's review caught: `conditioned = false` treats a null
// `conditioned` as NOT matching (nil never equals false), which silently
// suppressed the charge for every legacy clause forever; the fix compares
// `conditioned <> true` instead.
func TestClauseSatisfaction_LegacyNoConditionedField(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "legacyclause", "clause")
	f.aspect(t, "legacyclause", "terms", "clauseTerms", map[string]any{"kind": "computational", "amountCents": 4500.0, "period": "oneTime"})
	f.vtx(t, "legacyclause_acct", "account")
	f.edge(t, "chargesTo", "legacyclause", "legacyclause_acct")

	v := f.projectAt(t, "legacyclause")[0].Values
	require.Equal(t, true, v["missing_charge"], "a legacy clause with no conditioned field must still charge")
	require.Equal(t, true, v["violating"])
}

// TestClauseSatisfaction_Charged — an authorizedBy transaction exists: not
// violating, missing_charge false. Converged.
func TestClauseSatisfaction_Charged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkClause(t, "clause2", 4500)
	f.vtx(t, "tx1", "transaction")
	f.edge(t, "authorizedBy", "tx1", "clause2")

	v := f.projectAt(t, "clause2")[0].Values
	require.Equal(t, false, v["missing_charge"], "an authorizedBy transaction exists — converged")
	require.Equal(t, false, v["violating"])
}

// TestClauseSatisfaction_TwoClausesSameAccount — one row per clause anchor
// even when two clauses charge the same account (no fan-out cross-talk).
func TestClauseSatisfaction_TwoClausesSameAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "acct", "account")

	f.vtx(t, "clauseA", "clause")
	f.aspect(t, "clauseA", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1000.0, "period": "oneTime"})
	f.edge(t, "chargesTo", "clauseA", "acct")

	f.vtx(t, "clauseB", "clause")
	f.aspect(t, "clauseB", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 2000.0, "period": "oneTime"})
	f.edge(t, "chargesTo", "clauseB", "acct")
	f.vtx(t, "txB", "transaction")
	f.edge(t, "authorizedBy", "txB", "clauseB")

	va := f.projectAt(t, "clauseA")[0].Values
	require.Equal(t, true, va["missing_charge"], "clauseA has no charge of its own")
	require.Equal(t, 1000.0, va["amountCents"])

	vb := f.projectAt(t, "clauseB")[0].Values
	require.Equal(t, false, vb["missing_charge"], "clauseB's own charge converges it")
	require.Equal(t, 2000.0, vb["amountCents"])
}

// TestClauseSatisfaction_ConditionedFee_TargetLive — a conditionedOn link to
// a live vertex (e.g. a pet record): missing_charge behaves like an
// unconditioned clause while the condition holds.
func TestClauseSatisfaction_ConditionedFee_TargetLive(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "petclause", "clause")
	f.aspect(t, "petclause", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": true, "amountCents": 5000.0, "period": "oneTime"})
	f.vtx(t, "petclause_acct", "account")
	f.edge(t, "chargesTo", "petclause", "petclause_acct")
	f.vtx(t, "petclause_pet", "pet")
	f.edge(t, "conditionedOn", "petclause", "petclause_pet")

	v := f.projectAt(t, "petclause")[0].Values
	require.Equal(t, true, v["missing_charge"], "condition target is live and no charge yet — violating")
	require.Equal(t, true, v["violating"])
}

// TestClauseSatisfaction_ConditionedFee_TargetAbsent — the conditionedOn
// vertex was never linked (or has since been tombstoned, which the full
// engine's fetchNode already filters the same as absent — Contract #1):
// missing_charge stays false, the condition never holds.
func TestClauseSatisfaction_ConditionedFee_TargetAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "nopetclause", "clause")
	f.aspect(t, "nopetclause", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": true, "amountCents": 5000.0, "period": "oneTime"})
	f.vtx(t, "nopetclause_acct", "account")
	f.edge(t, "chargesTo", "nopetclause", "nopetclause_acct")
	// Deliberately no conditionedOn edge — the condition was declared
	// (conditioned=true) but its target vertex is gone, mirroring what a
	// tombstoned condition target looks like to this lens (fetchNode filters
	// isDeleted, so a tombstoned target and a never-linked one are
	// indistinguishable to the OPTIONAL MATCH).

	v := f.projectAt(t, "nopetclause")[0].Values
	require.Equal(t, false, v["missing_charge"], "conditioned but the target is gone — the fee never opens")
	require.Equal(t, false, v["violating"])
}

// TestClauseSatisfaction_JudgmentClause_Uninspected — an assigned inspector,
// no .inspection aspect yet: missing_inspection true, no amountCents/accountKey
// (a judgment clause charges nothing).
func TestClauseSatisfaction_JudgmentClause_Uninspected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "judgeclause", "clause")
	f.aspect(t, "judgeclause", "terms", "clauseTerms", map[string]any{"kind": "judgment", "conditioned": false, "period": "oneTime"})
	f.vtx(t, "judgeclause_insp", "identity")
	f.edge(t, "requiresInspectionBy", "judgeclause", "judgeclause_insp")

	v := f.projectAt(t, "judgeclause")[0].Values
	require.Equal(t, "vtx.identity."+f.ids["judgeclause_insp"], v["inspectorKey"])
	require.Nil(t, v["accountKey"], "a judgment clause charges no account")
	require.Nil(t, v["amountCents"])
	require.Equal(t, false, v["missing_charge"], "no account to charge — never gates on the charge axis")
	require.Equal(t, true, v["missing_inspection"], "no .inspection aspect yet — violating")
	require.Equal(t, true, v["violating"])
}

// TestClauseSatisfaction_JudgmentClause_Inspected — InspectPremises has
// recorded the .inspection aspect: missing_inspection false, converged.
func TestClauseSatisfaction_JudgmentClause_Inspected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "doneclause", "clause")
	f.aspect(t, "doneclause", "terms", "clauseTerms", map[string]any{"kind": "judgment", "conditioned": false, "period": "oneTime"})
	f.vtx(t, "doneclause_insp", "identity")
	f.edge(t, "requiresInspectionBy", "doneclause", "doneclause_insp")
	f.aspect(t, "doneclause", "inspection", "clauseInspection", map[string]any{"completed": true, "completedAt": "2026-07-02T12:00:00Z"})

	v := f.projectAt(t, "doneclause")[0].Values
	require.Equal(t, false, v["missing_inspection"], "the .inspection aspect exists — converged")
	require.Equal(t, false, v["violating"])
}

// TestClauseSatisfaction_Recurring_NeverCharged — a period=monthly clause
// with no .status.chargeValidUntil yet (never charged): missing_charge true
// (due immediately, same as a fresh oneTime clause), freshUntil null (no
// deadline to arm — Weaver's gap-dispatch owns it, not the temporal lane).
func TestClauseSatisfaction_Recurring_NeverCharged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "recurnew", "clause")
	f.aspect(t, "recurnew", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1500.0, "period": "monthly"})
	f.aspect(t, "recurnew", "status", "clauseStatus", map[string]any{"state": "active"})
	f.vtx(t, "recurnew_acct", "account")
	f.edge(t, "chargesTo", "recurnew", "recurnew_acct")

	v := f.projectAt(t, "recurnew")[0].Values
	require.Equal(t, true, v["missing_charge"], "never charged — due immediately")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no chargeValidUntil yet — nothing to arm")
}

// TestClauseSatisfaction_Recurring_Fresh — a period=monthly clause whose
// chargeValidUntil is still in the future: missing_charge false (converged
// for this period), freshUntil projects the same deadline to arm Weaver's
// temporal lane for the next re-open.
func TestClauseSatisfaction_Recurring_Fresh(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	f.vtx(t, "recurfresh", "clause")
	f.aspect(t, "recurfresh", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1500.0, "period": "monthly"})
	f.aspect(t, "recurfresh", "status", "clauseStatus", map[string]any{"state": "active", "chargeValidUntil": future})
	f.vtx(t, "recurfresh_acct", "account")
	f.edge(t, "chargesTo", "recurfresh", "recurfresh_acct")

	v := f.projectAt(t, "recurfresh")[0].Values
	require.Equal(t, false, v["missing_charge"], "chargeValidUntil is future — converged for this period")
	require.Equal(t, false, v["violating"])
	require.Equal(t, future, v["freshUntil"], "freshUntil must arm the same deadline")
}

// TestClauseSatisfaction_Recurring_Lapsed — a period=monthly clause whose
// chargeValidUntil is in the past: missing_charge re-opens, freshUntil goes
// null (the deadline already passed — gap-dispatch owns it now, not a timer).
func TestClauseSatisfaction_Recurring_Lapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	past := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	f.vtx(t, "recurlapsed", "clause")
	f.aspect(t, "recurlapsed", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1500.0, "period": "monthly"})
	f.aspect(t, "recurlapsed", "status", "clauseStatus", map[string]any{"state": "active", "chargeValidUntil": past})
	f.vtx(t, "recurlapsed_acct", "account")
	f.edge(t, "chargesTo", "recurlapsed", "recurlapsed_acct")

	v := f.projectAt(t, "recurlapsed")[0].Values
	require.Equal(t, true, v["missing_charge"], "chargeValidUntil lapsed — due again")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "a lapsed deadline is not re-armed")
}

// TestClauseSatisfaction_OneTime_FreshUntilAlwaysNull — a converged oneTime
// clause (Fire V1 shape, no period=monthly) never projects a freshUntil —
// the temporal lane is exclusively a monthly-clause behavior.
func TestClauseSatisfaction_OneTime_FreshUntilAlwaysNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkClause(t, "onetimefresh", 4500)
	f.vtx(t, "onetimefresh_tx", "transaction")
	f.edge(t, "authorizedBy", "onetimefresh_tx", "onetimefresh")

	v := f.projectAt(t, "onetimefresh")[0].Values
	require.Equal(t, false, v["missing_charge"])
	require.Nil(t, v["freshUntil"], "oneTime clauses never arm the temporal lane")
}
