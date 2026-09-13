package semanticcontracts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The clause's term through the real Processor: CreateClause storing a
// normalized validFrom/validUntil (and refusing a half term, a term on a
// oneTime clause, and an inverted one), and BackfillClauseTerm stamping the
// term onto an untermed monthly clause from its lease's .tenancy while
// moving the recorded due date onto the term's anniversary grid.

func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, local, class string, data map[string]any) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": false, "vertexKey": vertexKey, "localName": local, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vertexKey+"."+local, b); err != nil {
		t.Fatalf("seed aspect %s.%s: %v", vertexKey, local, err)
	}
}

// submitCreateClause publishes a CreateClause with the given raw payload
// fields (appended after leaseAppKey/accountKey) and drives it to the wanted
// outcome, returning the clause key it would mint.
func submitCreateClause(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, acctKey, extra string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","accountKey":"` + acctKey + `","prose":"Monthly rent per the signed lease agreement."` + extra + `}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return "vtx.clause." + nanoIDFromRequestID(reqID)
}

// submitBackfillClauseTerm publishes the leaseRentSettlement playbook's
// missing_term shape — Reads on the clause, its .terms, the lease and its
// .tenancy; .status as the absence-tolerant read; the clause's outbound
// governs walk as the declared enumeration — and drives it to the wanted
// outcome.
func submitBackfillClauseTerm(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, clauseKey, leaseAppKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "BackfillClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-09-13T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{clauseKey, clauseKey + ".terms", leaseAppKey, leaseAppKey + ".tenancy"},
			OptionalReads: []string{clauseKey + ".status"},
			Enumerations:  []processor.EnumerationHint{{Hub: clauseKey, Relation: "governs", Direction: "out"}},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

func readData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	data, _ := readDoc(t, ctx, conn, key)["data"].(map[string]any)
	return data
}

// TestCreateClause_WithTerm_StoresNormalizedTerm — a monthly clause minted
// with a term stores validFrom/validUntil on .terms in canonical UTC (an
// offset input is re-expressed), beside the rest of its terms.
func TestCreateClause_WithTerm_StoresNormalizedTerm(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausetermstore")

	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMSTREDHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermstor1", leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausetermst1", leaseKey, acctKey,
		`,"amountCents":240000,"period":"monthly","validFrom":"2026-09-08T02:00:00+02:00","validUntil":"2027-09-08T00:00:00Z"`,
		processor.OutcomeAccepted)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validFrom"].(string); got != "2026-09-08T00:00:00Z" {
		t.Fatalf("terms.validFrom = %q, want the canonical UTC form 2026-09-08T00:00:00Z", got)
	}
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("terms.validUntil = %q, want 2027-09-08T00:00:00Z", got)
	}
	if got, _ := terms["period"].(string); got != "monthly" {
		t.Fatalf("terms.period = %q, want monthly", got)
	}
	if got, _ := terms["amountCents"].(float64); got != 240000 {
		t.Fatalf("terms.amountCents = %v, want 240000", terms["amountCents"])
	}
}

// TestCreateClause_WithoutTerm_StoresNoTerm — the positive vector for the
// untermed shape: a monthly clause minted with neither field carries no
// validFrom/validUntil at all (the lens's missing_term reads their absence).
func TestCreateClause_WithoutTerm_StoresNoTerm(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausenoterm")

	leaseKey := seedLease(t, ctx, conn, "BBLEASENZTERMHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctnoterm001", leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausenoterm1", leaseKey, acctKey,
		`,"amountCents":1500,"period":"monthly"`, processor.OutcomeAccepted)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if _, ok := terms["validFrom"]; ok {
		t.Fatalf("an untermed clause must carry no validFrom, got %v", terms)
	}
	if _, ok := terms["validUntil"]; ok {
		t.Fatalf("an untermed clause must carry no validUntil, got %v", terms)
	}
}

// TestCreateClause_Term_Refusals — a half term (validFrom alone), a term on a
// oneTime clause, and an inverted term are each rejected.
func TestCreateClause_Term_Refusals(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausetermrefuse")

	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMREFHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermref01", leaseKey)

	cases := []struct{ name, label, extra string }{
		{"validFrom-alone", "createclausetermrf1", `,"amountCents":1500,"period":"monthly","validFrom":"2026-09-08T00:00:00Z"`},
		{"validUntil-alone", "createclausetermrf2", `,"amountCents":1500,"period":"monthly","validUntil":"2027-09-08T00:00:00Z"`},
		{"oneTime-clause", "createclausetermrf3", `,"amountCents":1500,"validFrom":"2026-09-08T00:00:00Z","validUntil":"2027-09-08T00:00:00Z"`},
		{"inverted", "createclausetermrf4", `,"amountCents":1500,"period":"monthly","validFrom":"2027-09-08T00:00:00Z","validUntil":"2026-09-08T00:00:00Z"`},
		{"empty-term", "createclausetermrf5", `,"amountCents":1500,"period":"monthly","validFrom":"2026-09-08T00:00:00Z","validUntil":"2026-09-08T00:00:00Z"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clauseKey := submitCreateClause(t, ctx, conn, cp, cons, tc.label, leaseKey, acctKey, tc.extra, processor.OutcomeRejected)
			if keyExists(t, ctx, conn, clauseKey) {
				t.Fatalf("%s: a refused CreateClause must mint nothing", tc.name)
			}
		})
	}
}

// backfillFixture mints an untermed monthly clause on a lease whose .tenancy
// is the given map, optionally pre-stamping the clause's recorded due date,
// and returns the clause key.
func backfillFixture(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, leaseID, label string, tenancy map[string]any, prevDue string) (string, string) {
	t.Helper()
	leaseKey := seedLease(t, ctx, conn, leaseID)
	if tenancy != nil {
		seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", tenancy)
	}
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacct"+label, leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclause"+label, leaseKey, acctKey,
		`,"amountCents":240000,"period":"monthly"`, processor.OutcomeAccepted)
	if prevDue != "" {
		seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", map[string]any{"state": "active", "chargeValidUntil": prevDue})
	}
	return clauseKey, leaseKey
}

func requireTerm(t *testing.T, ctx context.Context, conn *substrate.Conn, clauseKey, wantFrom, wantUntil, wantDue string) {
	t.Helper()
	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validFrom"].(string); got != wantFrom {
		t.Fatalf("terms.validFrom = %q, want %q", got, wantFrom)
	}
	if got, _ := terms["validUntil"].(string); got != wantUntil {
		t.Fatalf("terms.validUntil = %q, want %q", got, wantUntil)
	}
	if got, _ := terms["amountCents"].(float64); got != 240000 {
		t.Fatalf("terms.amountCents = %v, want 240000 preserved through the backfill", terms["amountCents"])
	}
	if got, _ := terms["period"].(string); got != "monthly" {
		t.Fatalf("terms.period = %q, want monthly preserved through the backfill", got)
	}
	if got, ok := terms["conditioned"].(bool); !ok || got {
		t.Fatalf("terms.conditioned = %v, want false preserved through the backfill", terms["conditioned"])
	}
	status := readData(t, ctx, conn, clauseKey+".status")
	if got, _ := status["chargeValidUntil"].(string); got != wantDue {
		t.Fatalf("status.chargeValidUntil = %q, want %q", got, wantDue)
	}
	wantState := "active"
	if wantDue >= wantUntil {
		wantState = "completed"
	}
	if got, _ := status["state"].(string); got != wantState {
		t.Fatalf("status.state = %q, want %s", got, wantState)
	}
}

// TestBackfillClauseTerm_NeverCharged_DueAtValidFrom — no recorded due: the
// term is the tenancy's [leaseStart, leaseEnd) and the first period is due at
// validFrom; every existing terms key is preserved.
func TestBackfillClauseTerm_NeverCharged_DueAtValidFrom(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill1")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNA", "bkfl01",
		map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z", "renewalOpensAt": "2027-08-08T00:00:00Z"}, "")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000001", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-09-08T00:00:00Z")
}

// TestBackfillClauseTerm_DueBeforeValidFrom_DueAtValidFrom — a recorded due
// before the term starts (every prior charge was pre-term) moves to
// validFrom.
func TestBackfillClauseTerm_DueBeforeValidFrom_DueAtValidFrom(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill2")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNB", "bkfl02",
		map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z"}, "2026-09-02T17:18:27Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000002", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-09-08T00:00:00Z")
}

// TestBackfillClauseTerm_DueInsideTerm_ContainingAnniversary — a recorded due
// inside the term (an off-grid 720h stamp: the last charge posted Sep 2) moves
// to the anniversary after the period that charge covered (Aug 31 – Sep 30),
// so the next charge bills the period the clause is in.
func TestBackfillClauseTerm_DueInsideTerm_ContainingAnniversary(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill3")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNC", "bkfl03",
		map[string]any{"leaseStart": "2026-05-31T00:00:00Z", "leaseEnd": "2027-05-31T00:00:00Z"}, "2026-10-02T17:18:27Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000003", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2026-05-31T00:00:00Z", "2027-05-31T00:00:00Z", "2026-09-30T00:00:00Z")
}

// TestBackfillClauseTerm_DueInsideTerm_ClampedAnniversary pins the
// day-of-month clamp in the containing-period search: a term from Jan 31
// whose last untermed charge posted Mar 5 (recorded due Apr 4, thirty days
// on) belongs to the period starting Feb 28 — the year/month digits alone
// say index 2 (Mar 31, past the charge), which the correction steps back to
// index 1 — so the next due is the following anniversary, Mar 31, computed
// from Jan 31 rather than from Feb 28.
func TestBackfillClauseTerm_DueInsideTerm_ClampedAnniversary(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill4")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMND", "bkfl04",
		map[string]any{"leaseStart": "2026-01-31T00:00:00Z", "leaseEnd": "2027-01-31T00:00:00Z"}, "2026-04-04T00:00:00Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000004", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2026-01-31T00:00:00Z", "2027-01-31T00:00:00Z", "2026-03-31T00:00:00Z")
}

// TestBackfillClauseTerm_ChargeOnTheFirstDayOfAPeriod_NextAnniversary — the
// last untermed charge posted a few hours into a 31-day period (Oct 1
// 07:55 on an Aug-1 grid; recorded due Oct 31 07:55). The period containing
// the recorded DUE is the one already billed, so normalizing from the due
// would re-bill October at once; normalizing from the charge instant lands
// on Nov 1.
func TestBackfillClauseTerm_ChargeOnTheFirstDayOfAPeriod_NextAnniversary(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill5")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNE", "bkfl05",
		map[string]any{"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2027-08-01T00:00:00Z"}, "2026-10-31T07:55:39Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000005", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2026-08-01T00:00:00Z", "2027-08-01T00:00:00Z", "2026-11-01T00:00:00Z")
}

// TestBackfillClauseTerm_DuePastValidUntil_CappedAtValidUntil — a recorded
// due at or past the term's end caps at validUntil: nothing is left to bill,
// and the lens reads that as no period left.
func TestBackfillClauseTerm_DuePastValidUntil_CappedAtValidUntil(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill6")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNF", "bkfl06",
		map[string]any{"leaseStart": "2025-09-06T17:01:31Z", "leaseEnd": "2026-09-06T17:01:31Z"}, "2026-10-05T09:00:00Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000006", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2025-09-06T17:01:31Z", "2026-09-06T17:01:31Z", "2026-09-06T17:01:31Z")
	status, _ := readDoc(t, ctx, conn, clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("a clause whose final period is already billed is completed, got state %q", got)
	}
	if _, ok := status["completedAt"]; !ok {
		t.Fatalf("completedAt must be stamped, got %v", status)
	}
}

// TestBackfillClauseTerm_RenewedLease_EndsAtTermStart — a lease that has been
// renewed carries termStart; the legacy clause covers the ORIGINAL term only,
// so its validUntil is termStart, not the extended leaseEnd.
func TestBackfillClauseTerm_RenewedLease_EndsAtTermStart(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill7")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNG", "bkfl07",
		map[string]any{"leaseStart": "2025-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z", "termStart": "2026-09-08T00:00:00Z", "rentAmount": 2600}, "")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000007", clauseKey, leaseKey, processor.OutcomeAccepted)
	requireTerm(t, ctx, conn, clauseKey, "2025-09-08T00:00:00Z", "2026-09-08T00:00:00Z", "2025-09-08T00:00:00Z")
}

// TestBackfillClauseTerm_AlreadyTermed_Rejected — a second backfill of the
// same clause, and a backfill of a clause minted with its term, are both
// refused: the term is written once.
func TestBackfillClauseTerm_AlreadyTermed_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill8")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNH", "bkfl08",
		map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z"}, "")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000008", clauseKey, leaseKey, processor.OutcomeAccepted)
	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000009", clauseKey, leaseKey, processor.OutcomeRejected)
	requireTerm(t, ctx, conn, clauseKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-09-08T00:00:00Z")

	acctKey := "vtx.account." + nanoIDFromRequestID(testutil.GenReqID("createacctbkfl08"))
	termed := submitCreateClause(t, ctx, conn, cp, cons, "createclausebkfl08b", leaseKey, acctKey,
		`,"amountCents":240000,"period":"monthly","validFrom":"2026-01-01T00:00:00Z","validUntil":"2027-01-01T00:00:00Z"`, processor.OutcomeAccepted)
	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000010", termed, leaseKey, processor.OutcomeRejected)
	terms := readData(t, ctx, conn, termed+".terms")
	if got, _ := terms["validFrom"].(string); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("a refused backfill must leave the minted term alone, got validFrom %q", got)
	}
}

// TestBackfillClauseTerm_NoTenancy_Rejected — the lease has no .tenancy to
// term the clause from: refused, the clause untouched.
func TestBackfillClauseTerm_NoTenancy_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill9")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNJ", "bkfl09", nil, "")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000011", clauseKey, leaseKey, processor.OutcomeRejected)
	terms := readData(t, ctx, conn, clauseKey+".terms")
	if _, ok := terms["validFrom"]; ok {
		t.Fatalf("a refused backfill must stamp no term, got %v", terms)
	}
}

// TestBackfillClauseTerm_OneTimeClause_Rejected — a term is monthly-only.
func TestBackfillClauseTerm_OneTimeClause_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill10")
	leaseKey := seedLease(t, ctx, conn, "BBLEASEBACKFLLHJKMNK")
	seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z"})
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctbkfl10", leaseKey)
	clauseKey := createClause(t, ctx, conn, cp, cons, "createclausebkfl10", leaseKey, acctKey, "Tenant agrees to a $45 lockout fee.", 4500)

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000012", clauseKey, leaseKey, processor.OutcomeRejected)
}

// TestBackfillClauseTerm_RepairsLegacyGovernsLinkKey — a clause whose governs
// link carries the legacy target segment (`governs.lease.<id>`, the shape
// every clause minted before the segment named the vertex type carries) has
// it re-keyed as it is termed: the Contract #1 key is created live with the
// same document, the legacy key is tombstoned, and the term lands as usual.
// The repair runs once — a second BackfillClauseTerm is AlreadyTermed.
func TestBackfillClauseTerm_RepairsLegacyGovernsLinkKey(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill11")
	const leaseID = "BBLEASEBACKFLLHJKMNL"
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, leaseID, "bkfl11",
		map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z"}, "")
	clauseID := clauseKey[len("vtx.clause."):]
	goodKey := "lnk.clause." + clauseID + ".governs.leaseapp." + leaseID
	legacyKey := "lnk.clause." + clauseID + ".governs.lease." + leaseID

	// Re-shape the minted link into the legacy key: remove the one
	// mint_clause wrote (a legacy clause never had it) and seed the legacy
	// spelling with the same document.
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, goodKey); err != nil {
		t.Fatalf("delete %s: %v", goodKey, err)
	}
	legacyDoc := map[string]any{"class": "governs", "isDeleted": false, "sourceVertex": clauseKey, "targetVertex": leaseKey, "localName": "governs", "data": map[string]any{}}
	b, _ := json.Marshal(legacyDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, legacyKey, b); err != nil {
		t.Fatalf("seed %s: %v", legacyKey, err)
	}
	if keyExists(t, ctx, conn, goodKey) || !keyExists(t, ctx, conn, legacyKey) {
		t.Fatalf("fixture: expected only the legacy key live before the backfill")
	}

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000013", clauseKey, leaseKey, processor.OutcomeAccepted)

	if !keyExists(t, ctx, conn, goodKey) {
		t.Fatalf("the Contract #1 governs key must be live after the backfill: %s", goodKey)
	}
	if keyExists(t, ctx, conn, legacyKey) {
		t.Fatalf("the legacy governs key must be tombstoned after the backfill: %s", legacyKey)
	}
	fixed := readDoc(t, ctx, conn, goodKey)
	if got, _ := fixed["sourceVertex"].(string); got != clauseKey {
		t.Fatalf("repaired link sourceVertex = %q, want %q", got, clauseKey)
	}
	if got, _ := fixed["targetVertex"].(string); got != leaseKey {
		t.Fatalf("repaired link targetVertex = %q, want %q", got, leaseKey)
	}
	if got, _ := fixed["class"].(string); got != "governs" {
		t.Fatalf("repaired link class = %q, want governs", got)
	}
	requireTerm(t, ctx, conn, clauseKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-09-08T00:00:00Z")

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000014", clauseKey, leaseKey, processor.OutcomeRejected)
}

// TestBackfillClauseTerm_ContractKeyedLink_LeftAlone — a clause whose governs
// link already names the leaseapp type is termed without any link write.
func TestBackfillClauseTerm_ContractKeyedLink_LeftAlone(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill12")
	const leaseID = "BBLEASEBACKFLLHJKMNM"
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, leaseID, "bkfl12",
		map[string]any{"leaseStart": "2026-09-08T00:00:00Z", "leaseEnd": "2027-09-08T00:00:00Z"}, "")
	clauseID := clauseKey[len("vtx.clause."):]
	goodKey := "lnk.clause." + clauseID + ".governs.leaseapp." + leaseID
	before := readDoc(t, ctx, conn, goodKey)

	submitBackfillClauseTerm(t, ctx, conn, cp, cons, "backfillterm0000015", clauseKey, leaseKey, processor.OutcomeAccepted)

	after := readDoc(t, ctx, conn, goodKey)
	if del, _ := after["isDeleted"].(bool); del {
		t.Fatalf("a Contract #1-keyed governs link must not be touched, got %v (before %v)", after, before)
	}
	if keyExists(t, ctx, conn, "lnk.clause."+clauseID+".governs.lease."+leaseID) {
		t.Fatalf("no legacy key may appear")
	}
	requireTerm(t, ctx, conn, clauseKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-09-08T00:00:00Z")
}

// TestBackfillClauseTerm_UndeclaredStatus_Rejected — the clause's .status
// exists but the dispatch did not declare it: the op refuses rather than
// treat "not hydrated" as "never charged" and rewind a charged clause to its
// first period.
func TestBackfillClauseTerm_UndeclaredStatus_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "backfill13")
	clauseKey, leaseKey := backfillFixture(t, ctx, conn, cp, cons, "BBLEASEBACKFLLHJKMNM", "bkfl13",
		map[string]any{"leaseStart": "2026-05-31T00:00:00Z", "leaseEnd": "2027-05-31T00:00:00Z"}, "2026-10-02T17:18:27Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("backfillterm0000013"),
		Lane:          processor.LaneDefault,
		OperationType: "BackfillClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-09-13T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:        []string{clauseKey, clauseKey + ".terms", leaseKey, leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{{Hub: clauseKey, Relation: "governs", Direction: "out"}},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if _, termed := readData(t, ctx, conn, clauseKey+".terms")["validFrom"]; termed {
		t.Fatal("a refused backfill must not term the clause")
	}
}
