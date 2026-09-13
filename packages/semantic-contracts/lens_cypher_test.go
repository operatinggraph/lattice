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
//   - TERMED MONTHLY: a clause carrying validFrom/validUntil bills one period
//     per calendar month inside the term, from a recorded lapse at each due
//     date, and nothing outside it; an untermed clause keeps the untermed rule.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type bcFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newBcFixture(t *testing.T) *bcFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &bcFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *bcFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
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

// edge indexes one link the way the live adjacency consumers do: the
// Contract #1 link key is built from each endpoint's vertex type and parsed
// back with substrate.ParseLinkKey, and the two directional entries come from
// adjacency.EventsForLink — so the OtherType a walk rebuilds the endpoint's
// key from is the link key's type segment, exactly as in production. A
// fixture that stamped OtherType from its own type map would let a lens walk
// bind through a link whose key names the wrong type, which live never does.
func (f *bcFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	f.edgeByKey(t, "lnk."+fromType+"."+fromID+"."+name+"."+toType+"."+toID)
}

// edgeByKey indexes a link from its full Contract #1 key alone.
func (f *bcFixture) edgeByKey(t *testing.T, linkKey string) {
	t.Helper()
	srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	require.Truef(t, ok, "not a Contract #1 link key: %s", linkKey)
	for _, evt := range adjacency.EventsForLink(linkKey, srcType, srcID, linkName, dstType, dstID, false, 1) {
		require.NoError(t, adjacency.Build(context.Background(), f.adjKV, evt))
	}
}

// projectAt runs the anchored clauseSatisfaction spec for one clause. NO clock
// parameter is supplied: the cypher references none — a monthly clause's
// freshness is the recorded lapse of its own charge window, not a wall-clock
// reading — and passing one would let a clock-reading regression pass unnoticed.
func (f *bcFixture) projectAt(t *testing.T, clauseName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(clauseSatisfactionSpec)
	require.NoError(t, err, "clauseSatisfaction cypher must parse on the full engine")
	clauseKey := "vtx.clause." + f.ids[clauseName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": clauseKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkMonthlyClause seeds one period=monthly computational clause with the given
// charge window (empty = never charged), linked to a charged account.
func (f *bcFixture) mkMonthlyClause(t *testing.T, name, chargeValidUntil string) {
	t.Helper()
	f.vtx(t, name, "clause")
	f.aspect(t, name, "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 1500.0, "period": "monthly"})
	status := map[string]any{"state": "active"}
	if chargeValidUntil != "" {
		status["chargeValidUntil"] = chargeValidUntil
	}
	f.aspect(t, name, "status", "clauseStatus", status)
	f.vtx(t, name+"_acct", "account")
	f.edge(t, "chargesTo", name, name+"_acct")
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits when a
// target's @at fires: the instant the timer fired for, recorded under that
// target's own key in byTarget, with expiredAt carrying the entity-wide maximum.
// A marker at or after the clause's chargeValidUntil is a recorded lapse of that
// window; one before it is a fire for an earlier window the current one has
// outrun.
func (f *bcFixture) recordLapse(t *testing.T, name string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, name, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
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
// a real bug caught in review: `conditioned = false` treats a null
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
	f.mkMonthlyClause(t, "recurnew", "")

	v := f.projectAt(t, "recurnew")[0].Values
	require.Equal(t, true, v["missing_charge"], "never charged — due immediately")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no chargeValidUntil yet — nothing to arm")
}

// TestClauseSatisfaction_Recurring_Fresh — a period=monthly clause no timer has
// fired on: missing_charge false (converged for this period), freshUntil projects
// the same deadline to arm Weaver's temporal lane for the next re-open. The
// window is deliberately in the PAST relative to any wall clock the suite runs
// at, so a clock-reading form would call this clause due — which is what makes
// the vector discriminating.
func TestClauseSatisfaction_Recurring_Fresh(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const chargeValidUntil = "2020-06-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurfresh", chargeValidUntil)

	v := f.projectAt(t, "recurfresh")[0].Values
	require.Equal(t, false, v["missing_charge"], "no recorded lapse — the charge still counts, whatever the wall clock says")
	require.Equal(t, false, v["violating"])
	require.Equal(t, chargeValidUntil, v["freshUntil"], "freshUntil must arm the same deadline")
}

// TestClauseSatisfaction_Recurring_Lapsed — the @at this lens armed fired and its
// lapse is recorded at chargeValidUntil: missing_charge re-opens, freshUntil goes
// null (there is nothing left to wait for — gap-dispatch owns it now, not a
// timer). The window is in the FAR FUTURE so only the recorded lapse can open the
// gap.
func TestClauseSatisfaction_Recurring_Lapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const chargeValidUntil = "2099-01-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurlapsed", chargeValidUntil)
	f.recordLapse(t, "recurlapsed", map[string]string{ClauseSatisfactionTarget: chargeValidUntil})

	v := f.projectAt(t, "recurlapsed")[0].Values
	require.Equal(t, true, v["missing_charge"], "a recorded lapse at chargeValidUntil — due again")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "a lapsed window is not re-armed")
}

// TestClauseSatisfaction_Recurring_ReChargedPastTheRecordedLapse is the RE-ARM
// vector, and the whole argument for comparing the marker against the deadline
// rather than testing its presence. Nothing clears the marker — MarkExpired never
// tombstones it — so a clause DebitAccount re-stamped with a later window must
// arm again off the stored comparison alone; a presence test would leave every
// re-charged monthly clause permanently due and charge it on every delivery.
func TestClauseSatisfaction_Recurring_ReChargedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const reCharged = "2099-06-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurrearmed", reCharged)
	f.recordLapse(t, "recurrearmed", map[string]string{ClauseSatisfactionTarget: "2026-06-01T00:00:00Z"})

	v := f.projectAt(t, "recurrearmed")[0].Values
	require.Equal(t, false, v["missing_charge"], "a lapse the current window has outrun is not a lapse of THIS window")
	require.Equal(t, reCharged, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestClauseSatisfaction_Recurring_PastWindowProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector, and the one place a "null when the
// deadline is already past" guard would be tempting. A clause whose charge window
// lapsed while no target was watching carries no marker, so it counts as charged
// to every consumer; the only thing that closes that is this row projecting the
// past instant, Weaver publishing the overdue @at, and NATS releasing it
// immediately. Nulling it here arms nothing and the clause is never re-charged.
func TestClauseSatisfaction_Recurring_PastWindowProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const longPast = "2020-06-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurstale", longPast)

	v := f.projectAt(t, "recurstale")[0].Values
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past chargeValidUntil with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_charge"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "recurstale", map[string]string{ClauseSatisfactionTarget: longPast})
	v = f.projectAt(t, "recurstale")[0].Values
	require.Equal(t, true, v["missing_charge"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestClauseSatisfaction_Recurring_BoundaryMarkerEqualsWindow pins which side of
// the `>=` the equal instant falls on: the timer fires AT chargeValidUntil and
// records that instant, so equality is the ordinary lapse rather than an edge
// case that leaves the clause armed forever.
func TestClauseSatisfaction_Recurring_BoundaryMarkerEqualsWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const chargeValidUntil = "2099-01-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurboundary", chargeValidUntil)
	f.recordLapse(t, "recurboundary", map[string]string{ClauseSatisfactionTarget: chargeValidUntil})

	v := f.projectAt(t, "recurboundary")[0].Values
	require.Equal(t, true, v["missing_charge"], "marker == chargeValidUntil is a lapse (>= boundary)")
	require.Nil(t, v["freshUntil"])
}

// TestClauseSatisfaction_Recurring_SiblingTargetLapseDoesNotOpenThisGap is the
// isolation vector. A clause is also the anchor of leaseRentSettlement, and both
// targets share one marker aspect, so reading the aspect's presence — or its
// entity-wide expiredAt maximum — would charge a clause off an unrelated fire.
func TestClauseSatisfaction_Recurring_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const chargeValidUntil = "2099-01-01T00:00:00Z"
	f.mkMonthlyClause(t, "recursibling", chargeValidUntil)
	f.recordLapse(t, "recursibling", map[string]string{LeaseRentSettlementTarget: "2100-01-01T00:00:00Z"})

	v := f.projectAt(t, "recursibling")[0].Values
	require.Equal(t, false, v["missing_charge"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, chargeValidUntil, v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestClauseSatisfaction_Recurring_MarkerWithNoByTargetMapReadsUnlapsed pins the
// shape a marker written before byTarget existed carries. `expiredAt` alone says
// which entity last lapsed, never which target, so a lens that read it would
// answer for a sibling's fire. The four-hop read resolves to nil and compareAny
// answers false: unlapsed, and the timer stays armed.
func TestClauseSatisfaction_Recurring_MarkerWithNoByTargetMapReadsUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	const chargeValidUntil = "2099-01-01T00:00:00Z"
	f.mkMonthlyClause(t, "recurlegacy", chargeValidUntil)
	f.aspect(t, "recurlegacy", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "2100-01-01T00:00:00Z"})

	v := f.projectAt(t, "recurlegacy")[0].Values
	require.Equal(t, false, v["missing_charge"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, chargeValidUntil, v["freshUntil"])
}

// TestClauseSatisfaction_ReferencesNoClockParameter is the structural half of the
// conversion, asserted on the compiled cypher rather than on any one row.
func TestClauseSatisfaction_ReferencesNoClockParameter(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(clauseSatisfactionSpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull, "clauseSatisfaction must compile to the full engine")
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		require.Truef(t, exhaustive, "the query shape must be provably free of $%s", param)
		require.Falsef(t, referenced,
			"clauseSatisfaction must reference no $%s — expiry is a recorded fact, not a clock reading", param)
	}
}

// TestClauseSatisfaction_ReadsItsOwnTargetsMarkerEntry binds the two halves that
// can silently drift apart: the §10.8 TargetID Weaver fires a timer under, and
// the byTarget key the lens compares against its window. A rename of one without
// the other leaves the lens reading an entry nothing ever writes — a gap that can
// never open, with every row still projecting and every seeded-marker test still
// passing.
func TestClauseSatisfaction_ReadsItsOwnTargetsMarkerEntry(t *testing.T) {
	require.Contains(t, clauseSatisfactionSpec, "byTarget."+ClauseSatisfactionTarget,
		"clauseSatisfaction must read the marker under its own target id — the timer that fires writes that entry and no other")
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

// projectLeaseAt runs the anchored leaseRentSettlement spec for one leaseapp.
func (f *bcFixture) projectLeaseAt(t *testing.T, leaseAppName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseRentSettlementSpec)
	require.NoError(t, err, "leaseRentSettlement cypher must parse on the full engine")
	leaseAppKey := "vtx.leaseapp." + f.ids[leaseAppName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": leaseAppKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkApprovedLeaseNoTerms seeds an approved leaseapp with no .terms aspect at
// all — the missing_terms shape (an application that skipped moveInDate, or
// one minted before requestedRent was captured).
func (f *bcFixture) mkApprovedLeaseNoTerms(t *testing.T, name string) {
	t.Helper()
	f.vtx(t, name, "leaseapp")
	f.aspect(t, name, "decision", "decision", map[string]any{"value": "approved"})
}

// mkApprovedLeaseWithTerms seeds an approved leaseapp carrying an agreed
// requestedRent, in dollars.
func (f *bcFixture) mkApprovedLeaseWithTerms(t *testing.T, name string, requestedRent float64) {
	t.Helper()
	f.mkApprovedLeaseNoTerms(t, name)
	f.aspect(t, name, "terms", "terms", map[string]any{"requestedRent": requestedRent})
}

// TestLeaseRentSettlement_ApprovedNoTerms_MissingTermsRow is the central
// vector: an approved lease with no requestedRent projects exactly one row,
// opens only missing_terms, and leaves missing_account/missing_clause closed
// — the account/clause gaps must never dispatch against a null rent.
func TestLeaseRentSettlement_ApprovedNoTerms_MissingTermsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkApprovedLeaseNoTerms(t, "notermslease")

	rows := f.projectLeaseAt(t, "notermslease")
	require.Len(t, rows, 1, "an approved lease with no requestedRent projects — the missing_terms shape")
	v := rows[0].Values
	require.Equal(t, true, v["missing_terms"], "no requestedRent — the rent itself is missing")
	require.Equal(t, false, v["missing_account"], "missing_account never opens while requestedRent is still null")
	require.Equal(t, false, v["missing_clause"], "missing_clause never opens while requestedRent is still null")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["termRentCents"], "no requestedRent to convert — the CASE WHEN keeps this column null rather than erroring the row")
}

// TestLeaseRentSettlement_ApprovedTermsWithoutRent_MissingTermsRow pins the
// other producible shape of a missing rent: a .terms aspect that exists
// (moveInDate recorded) but carries no requestedRent field. It must read
// exactly like an absent aspect.
func TestLeaseRentSettlement_ApprovedTermsWithoutRent_MissingTermsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkApprovedLeaseNoTerms(t, "termsnorent")
	f.aspect(t, "termsnorent", "terms", "terms", map[string]any{"moveInDate": "2026-10-01"})

	rows := f.projectLeaseAt(t, "termsnorent")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_terms"], "a .terms aspect with no requestedRent is still a missing rent")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_clause"])
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["termRentCents"])
}

// TestLeaseRentSettlement_ApprovedWithTermsNoAccount_MissingAccountUnchanged
// pins the shape once requestedRent is present: missing_account gates purely
// on the ledgerAccount guard aspect, and missing_clause stays closed until
// the account exists.
func TestLeaseRentSettlement_ApprovedWithTermsNoAccount_MissingAccountUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkApprovedLeaseWithTerms(t, "termsnoacct", 1500)

	v := f.projectLeaseAt(t, "termsnoacct")[0].Values
	require.Equal(t, false, v["missing_terms"], "requestedRent is present")
	require.Equal(t, true, v["missing_account"], "requestedRent present + no ledgerAccount")
	require.Equal(t, false, v["missing_clause"], "missing_clause never opens before the account exists")
	require.Equal(t, true, v["violating"])
	require.Equal(t, 150000.0, v["termRentCents"], "no renewal rent recorded — the term's rent is requestedRent, in cents")
}

// TestLeaseRentSettlement_NotApproved_NoTerms_NoRow — an undecided lease
// (no .decision aspect at all) never projects a row, terms or no terms: the
// population is gated on decision='approved' alone.
func TestLeaseRentSettlement_NotApproved_NoTerms_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "undecidedlease", "leaseapp")

	rows := f.projectLeaseAt(t, "undecidedlease")
	require.Empty(t, rows, "an undecided lease never projects, terms or no terms")
}

// mkTermedClause seeds one period=monthly computational clause carrying a
// term, with the given recorded due date (empty = never charged), linked to a
// charged account.
func (f *bcFixture) mkTermedClause(t *testing.T, name, validFrom, validUntil, chargeValidUntil string) {
	t.Helper()
	f.vtx(t, name, "clause")
	f.aspect(t, name, "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 240000.0,
		"period": "monthly", "validFrom": validFrom, "validUntil": validUntil})
	status := map[string]any{"state": "active"}
	if chargeValidUntil != "" {
		status["chargeValidUntil"] = chargeValidUntil
	}
	f.aspect(t, name, "status", "clauseStatus", status)
	f.vtx(t, name+"_acct", "account")
	f.edge(t, "chargesTo", name, name+"_acct")
}

const (
	termFrom  = "2026-09-08T00:00:00Z"
	termUntil = "2027-09-08T00:00:00Z"
	termDue1  = "2026-10-08T00:00:00Z"
	termDue11 = "2027-08-08T00:00:00Z"
)

// TestClauseSatisfaction_Termed_NotStarted — a termed clause with no recorded
// due and no lapse: nothing is due before the term starts, and freshUntil
// projects validFrom so Weaver's @at fires the start lapse. A clock-reading
// form would charge this clause (validFrom is in the past of any suite run);
// the recorded-fact form waits for the marker.
func TestClauseSatisfaction_Termed_NotStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termnew", termFrom, termUntil, "")

	v := f.projectAt(t, "termnew")[0].Values
	require.Equal(t, false, v["missing_charge"], "no lapse has reached validFrom — nothing is due yet")
	require.Equal(t, false, v["violating"])
	require.Equal(t, termFrom, v["freshUntil"], "the first due date arms the temporal lane")
	require.Equal(t, termFrom, v["validFrom"])
	require.Equal(t, termUntil, v["validUntil"])
}

// TestClauseSatisfaction_Termed_StartLapseOpensFirstPeriod — the @at fired at
// validFrom and recorded that instant: the first period is due (>= holds with
// equality, MarkExpired records the scheduled instant), no timer re-armed.
func TestClauseSatisfaction_Termed_StartLapseOpensFirstPeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termstart", termFrom, termUntil, "")
	f.recordLapse(t, "termstart", map[string]string{ClauseSatisfactionTarget: termFrom})

	v := f.projectAt(t, "termstart")[0].Values
	require.Equal(t, true, v["missing_charge"], "the recorded lapse reached validFrom — the first period is due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestClauseSatisfaction_Termed_ChargedWaitsForNextAnniversary — DebitAccount
// billed the first period and stamped the next due (validFrom + 1 month); the
// start lapse the marker still carries has been outrun, so nothing is due and
// freshUntil re-arms on the recorded due.
func TestClauseSatisfaction_Termed_ChargedWaitsForNextAnniversary(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termcharged", termFrom, termUntil, termDue1)
	f.recordLapse(t, "termcharged", map[string]string{ClauseSatisfactionTarget: termFrom})

	v := f.projectAt(t, "termcharged")[0].Values
	require.Equal(t, false, v["missing_charge"], "the lapse at validFrom does not reach the second period's due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, termDue1, v["freshUntil"], "the next anniversary arms the temporal lane")
	require.Equal(t, termDue1, v["chargeValidUntil"])
}

// TestClauseSatisfaction_Termed_LastPeriodDue — the recorded due is the last
// period's start and the lapse reached it: due (periodStart < validUntil).
func TestClauseSatisfaction_Termed_LastPeriodDue(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termlast", termFrom, termUntil, termDue11)
	f.recordLapse(t, "termlast", map[string]string{ClauseSatisfactionTarget: termDue11})

	v := f.projectAt(t, "termlast")[0].Values
	require.Equal(t, true, v["missing_charge"], "the twelfth period starts inside the term")
	require.Nil(t, v["freshUntil"])
}

// TestClauseSatisfaction_Termed_FinalPeriodBilledArmsNothing — the final
// charge stamped chargeValidUntil = validUntil: no period is left, nothing is
// due, and no timer is armed (a lapse recorded there could open nothing).
func TestClauseSatisfaction_Termed_FinalPeriodBilledArmsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termdone", termFrom, termUntil, termUntil)
	f.recordLapse(t, "termdone", map[string]string{ClauseSatisfactionTarget: termDue11})

	v := f.projectAt(t, "termdone")[0].Values
	require.Equal(t, false, v["missing_charge"], "the due has reached validUntil — the term is fully billed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "a fully billed term arms no timer")
}

// TestClauseSatisfaction_Termed_LapsePastValidUntilBillsNothing — a lapse
// recorded at or after the term's end, with the due sitting at validUntil,
// bills nothing: the inside-the-term conjunct fails closed even though the
// lapse comparison holds.
func TestClauseSatisfaction_Termed_LapsePastValidUntilBillsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkTermedClause(t, "termover", termFrom, termUntil, termUntil)
	f.recordLapse(t, "termover", map[string]string{ClauseSatisfactionTarget: "2027-10-01T00:00:00Z"})

	v := f.projectAt(t, "termover")[0].Values
	require.Equal(t, false, v["missing_charge"], "no period starts at or after validUntil")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestMintClause_GovernsLinkKeyNamesTheLeaseappType pins the link key
// CreateClause writes to the lease's Contract #1 vertex type (leaseapp). The
// adjacency index derives a walk's far endpoint from the link key's type
// segment and the engine rebuilds vtx.<type>.<id> from it, so a key spelled
// `governs.lease.` binds from the lease side only (its source segment,
// `clause`, is right) and never from the clause side — while every fixture
// here, which builds its keys from the vertex type, would still pass.
func TestMintClause_GovernsLinkKeyNamesTheLeaseappType(t *testing.T) {
	require.Contains(t, clauseDDLScript, `".governs.leaseapp."`,
		"mint_clause must write lnk.clause.<id>.governs.leaseapp.<id>")
	require.NotContains(t, clauseDDLScript, `".governs.lease."`,
		"the legacy target segment is what BackfillClauseTerm repairs, never what mint_clause writes")
}

// mkRentLease seeds an approved leaseapp with an agreed rent, a ledger
// account and the given .tenancy (nil = none) — the shape on which only
// missing_clause can still be open.
func (f *bcFixture) mkRentLease(t *testing.T, name string, requestedRent float64, tenancy map[string]any) {
	t.Helper()
	f.mkApprovedLeaseWithTerms(t, name, requestedRent)
	f.vtx(t, name+"_acct", "account")
	f.aspect(t, name, "ledgerAccount", "ledgerAccount", map[string]any{"accountKey": "vtx.account." + f.ids[name+"_acct"]})
	if tenancy != nil {
		f.aspect(t, name, "tenancy", "tenancy", tenancy)
	}
}

// mkRentClause seeds an unconditioned monthly clause governing the lease,
// termed from validFrom when non-empty.
func (f *bcFixture) mkRentClause(t *testing.T, name, leaseName, validFrom, validUntil string) {
	t.Helper()
	f.vtx(t, name, "clause")
	terms := map[string]any{"kind": "computational", "conditioned": false, "amountCents": 250000.0, "period": "monthly"}
	if validFrom != "" {
		terms["validFrom"] = validFrom
		terms["validUntil"] = validUntil
	}
	f.aspect(t, name, "terms", "clauseTerms", terms)
	f.edge(t, "governs", name, leaseName)
}

const (
	origStart = "2025-09-08T00:00:00Z"
	origEnd   = "2026-09-08T00:00:00Z"
	renewEnd  = "2027-09-08T00:00:00Z"
)

// TestLeaseRentSettlement_OriginalTerm_NoClause_MissingClause — an approved
// lease with rent, account and a never-renewed tenancy and no clause: the
// gap opens, and the dispatch's term columns are the original term at the
// agreed rent.
func TestLeaseRentSettlement_OriginalTerm_NoClause_MissingClause(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "origlease", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})

	v := f.projectLeaseAt(t, "origlease")[0].Values
	require.Equal(t, true, v["missing_clause"])
	require.Equal(t, false, v["missing_term"], "no clause at all — nothing to term")
	require.Nil(t, v["untermedClauseKey"])
	require.Equal(t, true, v["violating"])
	require.Equal(t, origStart, v["termStart"], "never renewed — the term starts at leaseStart")
	require.Equal(t, origStart, v["leaseStart"])
	require.Equal(t, origEnd, v["leaseEnd"])
	require.Equal(t, 250000.0, v["termRentCents"])
}

// TestLeaseRentSettlement_Renewed_OnlyOriginalClause_MissingClause — a
// signed renewal recorded termStart (= the old leaseEnd) and rentAmount, and
// the only clause is the original term's: the renewed term has no clause, so
// the gap opens with the renewed term and rent as its template columns.
func TestLeaseRentSettlement_Renewed_OnlyOriginalClause_MissingClause(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "renewlease", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": renewEnd, "termStart": origEnd, "rentAmount": 2600.0})
	f.mkRentClause(t, "renewlease_orig", "renewlease", origStart, origEnd)

	v := f.projectLeaseAt(t, "renewlease")[0].Values
	require.Equal(t, true, v["missing_clause"], "the original clause's validFrom is not the renewed term's start")
	require.Equal(t, origEnd, v["termStart"], "the renewed term starts where the original ended")
	require.Equal(t, renewEnd, v["leaseEnd"])
	require.Equal(t, 260000.0, v["termRentCents"], "the renewal's rent, in cents")
}

// TestLeaseRentSettlement_Renewed_UntermedClause_GapShut — the same renewed
// lease whose clause is still untermed (a legacy clause BackfillClauseTerm
// has not yet reached): the untermed count keeps the gap shut, so a renewal
// can never double-cover a period.
func TestLeaseRentSettlement_Renewed_UntermedClause_GapShut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "renewuntermed", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": renewEnd, "termStart": origEnd, "rentAmount": 2600.0})
	f.mkRentClause(t, "renewuntermed_legacy", "renewuntermed", "", "")

	v := f.projectLeaseAt(t, "renewuntermed")[0].Values
	require.Equal(t, false, v["missing_clause"], "an untermed clause holds the gap shut until it is termed")
	require.Equal(t, true, v["missing_term"], "and is itself the gap: the lease has a tenancy to term it from")
	require.Equal(t, "vtx.clause."+f.ids["renewuntermed_legacy"], v["untermedClauseKey"], "the BackfillClauseTerm param is the untermed clause")
	require.Equal(t, true, v["violating"])
}

// TestLeaseRentSettlement_Renewed_RenewalClausePresent_Converged — the
// renewed term's own clause exists (validFrom = termStart): converged, with
// the original term's clause still alongside it.
func TestLeaseRentSettlement_Renewed_RenewalClausePresent_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "renewdone", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": renewEnd, "termStart": origEnd, "rentAmount": 2600.0})
	f.mkRentClause(t, "renewdone_orig", "renewdone", origStart, origEnd)
	f.mkRentClause(t, "renewdone_renewal", "renewdone", origEnd, renewEnd)

	v := f.projectLeaseAt(t, "renewdone")[0].Values
	require.Equal(t, false, v["missing_clause"], "a clause whose validFrom equals termStart covers the current term")
	require.Equal(t, false, v["missing_term"], "both clauses carry their terms")
	require.Nil(t, v["untermedClauseKey"], "max() over an all-null CASE is null")
	require.Equal(t, false, v["violating"])
}

// TestLeaseRentSettlement_NoTenancy_NeverMintsAClause — an approved lease
// with rent and account but no .tenancy has no term to mint a clause for:
// missing_clause stays false (the dispatch would otherwise carry null
// validFrom/validUntil templates, which Weaver refuses).
func TestLeaseRentSettlement_NoTenancy_NeverMintsAClause(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "notenancylease", 2500, nil)

	v := f.projectLeaseAt(t, "notenancylease")[0].Values
	require.Equal(t, false, v["missing_terms"])
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_clause"], "no tenancy — no term to cover")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["termStart"])
	require.Nil(t, v["leaseEnd"])
}

// TestLeaseRentSettlement_OriginalTerm_OtherClausesNeverSuppress — a one-time
// fee and a conditioned monthly fee on the same lease are not the rent
// clause: the gap stays open.
func TestLeaseRentSettlement_OriginalTerm_OtherClausesNeverSuppress(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "otherlease", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})
	f.vtx(t, "otherlease_fee", "clause")
	f.aspect(t, "otherlease_fee", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 4500.0, "period": "oneTime"})
	f.edge(t, "governs", "otherlease_fee", "otherlease")
	f.vtx(t, "otherlease_pet", "clause")
	f.aspect(t, "otherlease_pet", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": true, "amountCents": 5000.0, "period": "monthly", "validFrom": origStart, "validUntil": origEnd})
	f.edge(t, "governs", "otherlease_pet", "otherlease")

	v := f.projectLeaseAt(t, "otherlease")[0].Values
	require.Equal(t, true, v["missing_clause"], "neither a one-time fee nor a conditioned fee is the rent clause")
}

// TestLeaseRentSettlement_UntermedClause_MissingTermViaLegacyLinkKey is the
// migration vector, seeded with the link key shape the live legacy clauses
// carry (`governs.lease.<id>`, the target segment naming a type no vertex
// has): the inbound walk from the lease resolves the clause from the key's
// SOURCE segment, so the untermed clause is found, missing_term opens with
// it as the dispatch param, and missing_clause stays shut. This is why the
// gap lives on the lease anchor.
func TestLeaseRentSettlement_UntermedClause_MissingTermViaLegacyLinkKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "legacylink", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})
	f.vtx(t, "legacylink_clause", "clause")
	f.aspect(t, "legacylink_clause", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 250000.0, "period": "monthly"})
	f.edgeByKey(t, "lnk.clause."+f.ids["legacylink_clause"]+".governs.lease."+f.ids["legacylink"])

	v := f.projectLeaseAt(t, "legacylink")[0].Values
	require.Equal(t, true, v["missing_term"], "the legacy-keyed link is walked from the lease side")
	require.Equal(t, "vtx.clause."+f.ids["legacylink_clause"], v["untermedClauseKey"])
	require.Equal(t, false, v["missing_clause"], "the untermed clause holds missing_clause shut — the two gaps are exclusive")
	require.Equal(t, true, v["violating"])
}

// TestLeaseRentSettlement_UntermedClause_NoTenancy_NoMissingTerm — an
// untermed clause on a lease with no .tenancy has nothing to be termed from:
// missing_term stays shut and the clause keeps the untermed cadence.
func TestLeaseRentSettlement_UntermedClause_NoTenancy_NoMissingTerm(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "untermednoten", 2500, nil)
	f.mkRentClause(t, "untermednoten_clause", "untermednoten", "", "")

	v := f.projectLeaseAt(t, "untermednoten")[0].Values
	require.Equal(t, "vtx.clause."+f.ids["untermednoten_clause"], v["untermedClauseKey"], "the clause is found")
	require.Equal(t, false, v["missing_term"], "but there is no tenancy to term it from")
	require.Equal(t, false, v["missing_clause"])
	require.Equal(t, false, v["violating"])
}

// TestLeaseRentSettlement_UntermedClause_OtherClausesNotCandidates — a
// one-time fee and a conditioned monthly fee, both untermed, are not rent
// clauses: neither is a missing_term candidate.
func TestLeaseRentSettlement_UntermedClause_OtherClausesNotCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "othersuntermed", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})
	f.mkRentClause(t, "othersuntermed_rent", "othersuntermed", origStart, origEnd)
	f.vtx(t, "othersuntermed_fee", "clause")
	f.aspect(t, "othersuntermed_fee", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 4500.0, "period": "oneTime"})
	f.edge(t, "governs", "othersuntermed_fee", "othersuntermed")
	f.vtx(t, "othersuntermed_pet", "clause")
	f.aspect(t, "othersuntermed_pet", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": true, "amountCents": 5000.0, "period": "monthly"})
	f.edge(t, "governs", "othersuntermed_pet", "othersuntermed")

	v := f.projectLeaseAt(t, "othersuntermed")[0].Values
	require.Equal(t, false, v["missing_term"])
	require.Nil(t, v["untermedClauseKey"])
	require.Equal(t, false, v["missing_clause"])
	require.Equal(t, false, v["violating"])
}
