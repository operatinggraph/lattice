package loftspaceledger_test

// ReturnDeposit through the real Processor pipeline: the credit a charged
// security deposit comes back as once the tenancy has ended, the chain of
// custody it records (postedTo + authorizedBy), the clause's .status moving to
// returned under OCC with every field DebitAccount left, the .arrears stale
// mark every entry lands, the idempotent no-op on a clause already returned,
// every refusal, and the derive_reads proof — a submitter that declares
// nothing still has the whole read set hydrated.
//
// The deposit clause is a semantic-contracts vertex; that package depends on
// this one, so the clause and its aspects/links are seeded directly in the
// shape CreateClause + DebitAccount leave them in (mint_clause's .terms with
// purpose=deposit, DebitAccount's completed .status, the two custody links
// with the clause as source).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	depositAmountCents = 250000
	depositChargedAt   = "2026-07-02T13:00:00Z"
	depositReturnAt    = "2027-07-03T09:00:00Z"
	tenancyEndedAt     = "2027-07-01T00:00:00Z"
)

// depositFixture is one lease + its account + one deposit clause, seeded in
// the state DebitAccount's charge leaves the clause in, with the lease's
// tenancy ended — the state missing_depositReturn dispatches on. Each option
// below moves one fact off that state for the refusal it proves.
type depositFixture struct {
	leaseKey, acctKey, clauseKey string
}

type depositOption func(*depositSeed)

type depositSeed struct {
	purpose          string
	period, kind     string
	status           map[string]any
	endedAt          string
	chargesToAcct    string
	governsLease     string
	chargesToDeleted bool
	arrears          map[string]any
}

func withPeriod(p string) depositOption { return func(s *depositSeed) { s.period = p } }
func withKind(k string) depositOption   { return func(s *depositSeed) { s.kind = k } }
func withChargesToTombstoned() depositOption {
	return func(s *depositSeed) { s.chargesToDeleted = true }
}

func withPurpose(p string) depositOption { return func(s *depositSeed) { s.purpose = p } }
func withStatus(st map[string]any) depositOption {
	return func(s *depositSeed) { s.status = st }
}
func withEndedAt(at string) depositOption { return func(s *depositSeed) { s.endedAt = at } }
func withChargesTo(acct string) depositOption {
	return func(s *depositSeed) { s.chargesToAcct = acct }
}
func withGoverns(lease string) depositOption {
	return func(s *depositSeed) { s.governsLease = lease }
}
func withArrears(a map[string]any) depositOption { return func(s *depositSeed) { s.arrears = a } }

// seedDepositFixture opens the lease's account through the real op and seeds
// the clause around it. leaseID / clauseID are the fixture's own NanoIDs.
func seedDepositFixture(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, leaseID, clauseID string, opts ...depositOption) depositFixture {
	t.Helper()
	seed := &depositSeed{
		purpose: "deposit",
		period:  "oneTime",
		kind:    "computational",
		status:  map[string]any{"state": "completed", "completedAt": depositChargedAt, "chargeValidUntil": "2026-08-01T13:00:00Z"},
		endedAt: tenancyEndedAt,
	}
	for _, o := range opts {
		o(seed)
	}
	leaseKey := seedLease(t, ctx, conn, leaseID)
	tenancy := map[string]any{"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z"}
	if seed.endedAt != "" {
		tenancy["endedAt"] = seed.endedAt
	}
	seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", tenancy)
	acctKey := createAccount(t, ctx, conn, cp, cons, label, leaseKey)

	clauseKey := "vtx.clause." + clauseID
	seedVertex(t, ctx, conn, clauseKey, "clause", nil)
	terms := map[string]any{"kind": seed.kind, "conditioned": false, "amountCents": depositAmountCents, "period": seed.period}
	if seed.purpose != "" {
		terms["purpose"] = seed.purpose
	}
	seedAspect(t, ctx, conn, clauseKey, "terms", "clauseTerms", terms)
	seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", seed.status)

	chargesTo := acctKey
	if seed.chargesToAcct != "" {
		chargesTo = seed.chargesToAcct
	}
	chargesToKey := "lnk.clause." + clauseID + ".chargesTo.account." + chargesTo[len("vtx.account."):]
	seedLink(t, ctx, conn, chargesToKey, clauseKey, chargesTo, "chargesTo", "chargesTo")
	if seed.chargesToDeleted {
		// The link's tombstone: the same document with isDeleted true — what
		// a repointed or retracted custody link leaves under the key.
		doc := map[string]any{"class": "chargesTo", "isDeleted": true, "sourceVertex": clauseKey, "targetVertex": chargesTo, "localName": "chargesTo", "data": map[string]any{}}
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, chargesToKey, b); err != nil {
			t.Fatalf("tombstone link %s: %v", chargesToKey, err)
		}
	}
	governs := leaseKey
	if seed.governsLease != "" {
		governs = seed.governsLease
	}
	seedLink(t, ctx, conn, "lnk.clause."+clauseID+".governs.leaseapp."+governs[len("vtx.leaseapp."):],
		clauseKey, governs, "governs", "governs")
	if seed.arrears != nil {
		seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", seed.arrears)
	}
	return depositFixture{leaseKey: leaseKey, acctKey: acctKey, clauseKey: clauseKey}
}

// returnDepositHint is the contextHint leaseRentSettlement's
// missing_depositReturn dispatches with (semantic-contracts targets.go): the
// account, the lease's .tenancy, the clause, its .terms and .status as
// required reads, the account's .arrears absence-tolerant. The two custody
// links are the DDL's own derive_reads' to supply.
func returnDepositHint(f depositFixture) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{f.acctKey, f.leaseKey + ".tenancy", f.clauseKey, f.clauseKey + ".terms", f.clauseKey + ".status"},
		OptionalReads: []string{f.acctKey + ".arrears"},
	}
}

// submitReturnDeposit drives one ReturnDeposit{leaseAppKey, clauseKey,
// accountKey} with the given hint (nil = the bare shape, declaring nothing),
// asserts the outcome and returns the reply + the transaction key the request
// would mint.
func submitReturnDeposit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label string, leaseKey, clauseKey, acctKey string, hint *processor.ContextHint,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReturnDeposit",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		Class:         "transaction",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","clauseKey":"` + clauseKey + `","accountKey":"` + acctKey + `"}`),
		ContextHint:   hint,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, "vtx.transaction." + nanoIDFromRequestID(reqID)
}

func requireRefusal(t *testing.T, reply *processor.OperationReply, code string) {
	t.Helper()
	if reply.Error == nil || !strings.Contains(reply.Error.Message, code) {
		t.Fatalf("want a %s refusal, got %+v", code, reply.Error)
	}
}

// requireDepositReturned asserts the whole shape one accepted return leaves:
// the credit entry, both custody links, the clause's .status returned with
// DebitAccount's fields kept.
func requireDepositReturned(t *testing.T, ctx context.Context, conn *substrate.Conn, f depositFixture, txKey string) {
	t.Helper()
	txID := txKey[len("vtx.transaction."):]
	if !keyExists(t, ctx, conn, txKey) {
		t.Fatalf("the return must mint %s", txKey)
	}
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["type"].(string); got != "credit" {
		t.Fatalf("entry.type = %q, want credit", got)
	}
	if got, _ := entry["amountCents"].(float64); got != depositAmountCents {
		t.Fatalf("entry.amountCents = %v, want %d — the clause's own amount", entry["amountCents"], depositAmountCents)
	}
	if got, _ := entry["postedAt"].(string); got != depositReturnAt {
		t.Fatalf("entry.postedAt = %q, want %s", got, depositReturnAt)
	}
	if got, _ := entry["memo"].(string); got != "Security deposit returned" {
		t.Fatalf("entry.memo = %q", got)
	}
	acctID := f.acctKey[len("vtx.account."):]
	clauseID := f.clauseKey[len("vtx.clause."):]
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".postedTo.account."+acctID) {
		t.Fatalf("postedTo link must exist")
	}
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".authorizedBy.clause."+clauseID) {
		t.Fatalf("authorizedBy link must exist — the chain of custody back to the deposit clause")
	}
	status, _ := readDoc(t, ctx, conn, f.clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["state"].(string); got != "returned" {
		t.Fatalf("clause status.state = %q, want returned", got)
	}
	if got, _ := status["returnedAt"].(string); got != depositReturnAt {
		t.Fatalf("clause status.returnedAt = %q, want the credit's postedAt %s", got, depositReturnAt)
	}
	if got, _ := status["completedAt"].(string); got != depositChargedAt {
		t.Fatalf("clause status.completedAt = %q — the return must keep every field DebitAccount left", got)
	}
	if got, _ := status["chargeValidUntil"].(string); got != "2026-08-01T13:00:00Z" {
		t.Fatalf("clause status.chargeValidUntil = %q — the return must keep every field DebitAccount left", got)
	}
}

// TestReturnDeposit_CreditsTheDepositAndMarksTheClauseReturned is the
// positive vector every refusal below is measured against: the
// Weaver-shaped dispatch on a charged deposit clause whose tenancy has ended
// posts the credit, links it, and moves the clause to returned.
func TestReturnDeposit_CreditsTheDepositAndMarksTheClauseReturned(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "returndeposit")

	f := seedDepositFixture(t, ctx, conn, cp, cons, "bbretdepacct00000001", "BBRETDEPLEASEHJKMNPQ", "BBRETDEPCLAUSEHJKMNP")
	_, txKey := submitReturnDeposit(t, ctx, conn, cp, cons, "bbretdep000000000001", f.leaseKey, f.clauseKey, f.acctKey,
		returnDepositHint(f), processor.OutcomeAccepted)
	requireDepositReturned(t, ctx, conn, f, txKey)
	if keyExists(t, ctx, conn, f.acctKey+".arrears") {
		t.Fatalf("an account with no arrears state must gain none — the mark is never minted where nothing exists")
	}
}

// TestReturnDeposit_AlreadyReturned_IsANoOp — a clause already returned is
// the EndTenancy idempotent shape: accepted, no mutation, no transaction, the
// status untouched. The lens drops a returned clause from candidacy, so this
// is a race with an earlier return, never a second refund.
func TestReturnDeposit_AlreadyReturned_IsANoOp(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "returndepositnoop")

	f := seedDepositFixture(t, ctx, conn, cp, cons, "bbretdepnoopacct0001", "BBRETDEPNQQPLEASEHJK", "BBRETDEPNQQPCLAUSEHJ",
		withStatus(map[string]any{"state": "returned", "completedAt": depositChargedAt, "returnedAt": "2027-07-02T09:00:00Z"}))
	reply, txKey := submitReturnDeposit(t, ctx, conn, cp, cons, "bbretdepnoop00000001", f.leaseKey, f.clauseKey, f.acctKey,
		returnDepositHint(f), processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("a no-op is accepted, not refused: %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, txKey) {
		t.Fatalf("a second return must mint no transaction")
	}
	status, _ := readDoc(t, ctx, conn, f.clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["returnedAt"].(string); got != "2027-07-02T09:00:00Z" {
		t.Fatalf("the no-op must leave the recorded returnedAt alone, got %q", got)
	}
}

// TestReturnDeposit_MarksArrearsStale — the credit moves the FIFO the
// arrears evaluation ages, so an account carrying arrears state has it marked
// stale with every other field carried, exactly as every post_entry credit
// does.
func TestReturnDeposit_MarksArrearsStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "returndepositarrears")

	f := seedDepositFixture(t, ctx, conn, cp, cons, "bbretdeparracct00001", "BBRETDEPARRLEASEHJKM", "BBRETDEPARRCLAUSEHJK",
		withArrears(map[string]any{
			"evaluatedAt": "2027-06-10T09:00:00Z", "dueAt": "2027-06-01T00:00:00Z", "remindAt": "2027-06-06T00:00:00Z",
			"remindedFor": "2027-06-01T00:00:00Z", "sentAt": "2027-06-10T09:00:00Z", "historyTooLong": true,
		}))
	_, txKey := submitReturnDeposit(t, ctx, conn, cp, cons, "bbretdeparr000000001", f.leaseKey, f.clauseKey, f.acctKey,
		returnDepositHint(f), processor.OutcomeAccepted)
	requireDepositReturned(t, ctx, conn, f, txKey)
	arrears := arrearsData(t, ctx, conn, f.acctKey)
	if stale, _ := arrears["stale"].(bool); !stale {
		t.Fatalf("the return credit must mark .arrears stale: %+v", arrears)
	}
	if got, _ := arrears["sentAt"].(string); got != "2027-06-10T09:00:00Z" {
		t.Fatalf("the send record must be carried across the mark, got %q", got)
	}
	if _, ok := arrears["historyTooLong"]; ok {
		t.Fatalf("historyTooLong is dropped by every posted entry, buying one more evaluation: %+v", arrears)
	}
}

// TestReturnDeposit_UndeclaredSubmitter_StillReturns is the derive_reads
// proof: a submitter that declares NOTHING — no Reads, no OptionalReads — has
// the account, the clause, its .terms and .status, the lease's .tenancy, the
// two custody links and the account's .arrears hydrated by the DDL's own
// derivation, so the same return lands (the read-drift guard armed on the
// pipeline fails the test at any lazy read). The links are the reason this
// channel exists: no dispatcher can template a key spanning two payload
// fields, so a return that proves custody at all proves it off derived reads.
func TestReturnDeposit_UndeclaredSubmitter_StillReturns(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "returndepositbare")

	f := seedDepositFixture(t, ctx, conn, cp, cons, "bbretdepbareacct0001", "BBRETDEPBARELEASEHJK", "BBRETDEPBARECLAUSEHJ",
		withArrears(map[string]any{"evaluatedAt": "2027-06-10T09:00:00Z"}))
	reqID := testutil.GenReqID("bbretdepbare00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReturnDeposit",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		Payload:       json.RawMessage(`{"leaseAppKey":"` + f.leaseKey + `","clauseKey":"` + f.clauseKey + `","accountKey":"` + f.acctKey + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	requireDepositReturned(t, ctx, conn, f, "vtx.transaction."+nanoIDFromRequestID(reqID))
	arrears := arrearsData(t, ctx, conn, f.acctKey)
	if stale, _ := arrears["stale"].(bool); !stale {
		t.Fatalf("the stale mark must land on a derived, conditioned key even when the submitter declared nothing: %+v", arrears)
	}
}

// TestReturnDeposit_Refusals — each fact the return rides on, moved off the
// charged-and-ended state one at a time: the bare submission shape, so every
// refusal is the script's own code rather than a hydration miss on a
// declared-required key. The two returned-but-misaddressed cases pin the
// order: a clause already returned is a no-op only once the lease, the
// tenancy and the custody links check out — a mis-addressed hand submit
// refuses, never reads as done.
func TestReturnDeposit_Refusals(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "returndepositrefuse")

	otherLease := seedLease(t, ctx, conn, "BBRETDEPQTHERLEASEHJ")
	otherAcct := createAccount(t, ctx, conn, cp, cons, "bbretdepotheracct001", otherLease)

	cases := []struct {
		name, label, leaseID, clauseID, code string
		opts                                 []depositOption
		useLease, useClause, useAcct         string
	}{
		{name: "unknown-account", label: "bbretdepref000000001", leaseID: "BBRETDEPREFLEASE1HJK", clauseID: "BBRETDEPREFCLAUSE1HJ", code: "UnknownAccount",
			useAcct: "vtx.account.BBRETDEPNQSUCHACCTHJ"},
		{name: "unknown-clause", label: "bbretdepref000000002", leaseID: "BBRETDEPREFLEASE2HJK", clauseID: "BBRETDEPREFCLAUSE2HJ", code: "UnknownClause",
			useClause: "vtx.clause.BBRETDEPNQSUCHCLSEHJ"},
		{name: "unknown-lease", label: "bbretdepref000000009", leaseID: "BBRETDEPREFLEASE9HJK", clauseID: "BBRETDEPREFCLAUSE9HJ", code: "UnknownLeaseApplication",
			useLease: "vtx.leaseapp.BBRETDEPNQSUCHLEASEH"},
		{name: "not-a-deposit-monthly", label: "bbretdepref000000010", leaseID: "BBRETDEPREFLEASEAHJK", clauseID: "BBRETDEPREFCLAUSEAHJ", code: "NotADeposit",
			opts: []depositOption{withPeriod("monthly")}},
		{name: "not-a-deposit-judgment", label: "bbretdepref000000011", leaseID: "BBRETDEPREFLEASEBHJK", clauseID: "BBRETDEPREFCLAUSEBHJ", code: "NotADeposit",
			opts: []depositOption{withKind("judgment")}},
		{name: "clause-chargesTo-link-tombstoned", label: "bbretdepref000000012", leaseID: "BBRETDEPREFLEASECHJK", clauseID: "BBRETDEPREFCLAUSECHJ", code: "ClauseAccountMismatch",
			opts: []depositOption{withChargesToTombstoned()}},
		{name: "returned-but-misaddressed-lease", label: "bbretdepref000000013", leaseID: "BBRETDEPREFLEASEDHJK", clauseID: "BBRETDEPREFCLAUSEDHJ", code: "ClauseLeaseMismatch",
			opts: []depositOption{withStatus(map[string]any{"state": "returned", "returnedAt": "2027-07-02T09:00:00Z"}), withGoverns(otherLease)}},
		{name: "returned-but-tenancy-not-ended", label: "bbretdepref000000014", leaseID: "BBRETDEPREFLEASEEHJK", clauseID: "BBRETDEPREFCLAUSEEHJ", code: "TenancyNotEnded",
			opts: []depositOption{withStatus(map[string]any{"state": "returned", "returnedAt": "2027-07-02T09:00:00Z"}), withEndedAt("")}},
		{name: "not-a-deposit-no-purpose", label: "bbretdepref000000003", leaseID: "BBRETDEPREFLEASE3HJK", clauseID: "BBRETDEPREFCLAUSE3HJ", code: "NotADeposit",
			opts: []depositOption{withPurpose("")}},
		{name: "not-a-deposit-other-purpose", label: "bbretdepref000000004", leaseID: "BBRETDEPREFLEASE4HJK", clauseID: "BBRETDEPREFCLAUSE4HJ", code: "NotADeposit",
			opts: []depositOption{withPurpose("petFee")}},
		{name: "deposit-not-charged", label: "bbretdepref000000005", leaseID: "BBRETDEPREFLEASE5HJK", clauseID: "BBRETDEPREFCLAUSE5HJ", code: "DepositNotCharged",
			opts: []depositOption{withStatus(map[string]any{"state": "active"})}},
		{name: "tenancy-not-ended", label: "bbretdepref000000006", leaseID: "BBRETDEPREFLEASE6HJK", clauseID: "BBRETDEPREFCLAUSE6HJ", code: "TenancyNotEnded",
			opts: []depositOption{withEndedAt("")}},
		{name: "clause-charges-another-account", label: "bbretdepref000000007", leaseID: "BBRETDEPREFLEASE7HJK", clauseID: "BBRETDEPREFCLAUSE7HJ", code: "ClauseAccountMismatch",
			opts: []depositOption{withChargesTo(otherAcct)}},
		{name: "clause-governs-another-lease", label: "bbretdepref000000008", leaseID: "BBRETDEPREFLEASE8HJK", clauseID: "BBRETDEPREFCLAUSE8HJ", code: "ClauseLeaseMismatch",
			opts: []depositOption{withGoverns(otherLease)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// GenReqID keeps a label's first 20 characters, so the account's
			// label must differ from the return's inside them.
			acctLabel := strings.Replace(tc.label, "bbretdepref", "bbretdepacc", 1)
			f := seedDepositFixture(t, ctx, conn, cp, cons, acctLabel, tc.leaseID, tc.clauseID, tc.opts...)
			leaseKey, clauseKey, acctKey := f.leaseKey, f.clauseKey, f.acctKey
			if tc.useLease != "" {
				leaseKey = tc.useLease
			}
			if tc.useClause != "" {
				clauseKey = tc.useClause
			}
			if tc.useAcct != "" {
				acctKey = tc.useAcct
			}
			reply, txKey := submitReturnDeposit(t, ctx, conn, cp, cons, tc.label, leaseKey, clauseKey, acctKey, nil, processor.OutcomeRejected)
			requireRefusal(t, reply, tc.code)
			if keyExists(t, ctx, conn, txKey) {
				t.Fatalf("%s: a refused return must mint nothing", tc.name)
			}
			// The seeded-returned cases keep their seeded returnedAt; every
			// other case must not have been marked returned by a refused submit.
			status, _ := readDoc(t, ctx, conn, f.clauseKey+".status")["data"].(map[string]any)
			if got, _ := status["returnedAt"].(string); got == depositReturnAt {
				t.Fatalf("%s: a refused return must not mark the clause returned", tc.name)
			}
		})
	}
}
