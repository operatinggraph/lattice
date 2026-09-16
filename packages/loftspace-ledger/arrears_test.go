package loftspaceledger_test

// The rent-arrears reminder, end to end through the real Processor pipeline:
// the .arrears episode aspect EvaluateLoftspaceArrears mints and rewrites, the
// stale mark every posted entry adds, the one-notification-per-EPISODE
// guarantee, the actor guard, the two-hop tenant resolution, the replay-budget
// degrade, and the bridge's replyOp. Mirrors wellness-ledger's TestArrears_*
// set (a ledger that likewise stores no balance, so a posted entry never opens
// or ends an episode itself) minus its refund-netting vectors — no entry in
// this ledger names a charge it reverses — plus what is LoftSpace's own: the
// head's recorded due date is the fact aged (its postedAt only when it
// recorded none), and the reminder waits out a five-day grace after it.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
)

// arrearsWeaverCapDoc is the grant Weaver's dispatch actor holds for the
// evaluation — the same operator/Scope:"any" row the package mints
// (permissions.go).
func arrearsWeaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "EvaluateLoftspaceArrears", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// arrearsHint is the contextHint the loftspaceArrearsReminders playbook
// dispatches with (targets.go): the account root, its absence-tolerant
// .arrears aspect, and the bounded postedTo replay + the heldFor walk that
// starts the tenant resolution. The per-transaction .entry reads, the lease
// root read and the lease's applicationFor hop that walk discovers are NOT
// declared — their keys are data-derived, the class-(e) split.
func arrearsHint(acctKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{acctKey},
		OptionalReads: []string{acctKey + ".arrears"},
		Enumerations: []processor.EnumerationHint{
			{Hub: acctKey, Relation: "postedTo", Direction: "in"},
			{Hub: acctKey, Relation: "heldFor", Direction: "out"},
		},
	}
}

// evaluateArrears drives one EvaluateLoftspaceArrears as `actor` at
// `submittedAt`, asserts the outcome, and returns the reply (for a refusal's
// message) and the request id (for the outbox the notification rides on).
// Class is LEFT EMPTY, exactly as Weaver's actuator dispatches a directOp — it
// relies on the Processor's operationType→class reverse index, which resolves
// to the account vertexType handler.
func evaluateArrears(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actor, acctKey, submittedAt string,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateLoftspaceArrears",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `"}`),
		ContextHint:   arrearsHint(acctKey),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, reqID
}

// arrearsData reads the account's .arrears aspect back. Returns nil when the
// aspect is absent — which is a real state (an account nothing has evaluated
// carries none), not an error.
func arrearsData(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	if !keyExists(t, ctx, conn, acctKey+".arrears") {
		return nil
	}
	doc := readDoc(t, ctx, conn, acctKey+".arrears")
	if cls, _ := doc["class"].(string); cls != "loftspaceAccountArrears" {
		t.Fatalf("%s.arrears class = %q, want loftspaceAccountArrears", acctKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.arrears carries no data", acctKey)
	}
	return data
}

// arrearsNotification returns the external.notification event this request's
// own transactional outbox carries, or nil when it emitted none. "Nil" is the
// assertion half the once-per-episode guarantee rests on.
func arrearsNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(requestID))
	if err != nil {
		t.Fatalf("read outbox aspect for %s: %v", requestID, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect for %s: %v", requestID, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == "external.notification" {
			return e.Payload
		}
	}
	return nil
}

// postEntryAt posts one entry of opType at an explicit instant — the arrears
// vectors turn on WHEN a charge posted. Declares the account alone, exactly as
// every existing dispatcher of these ops does; the .arrears key is the DDL's
// own derive_reads' to supply.
func postEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, opType, acctKey, submittedAt string, amountCents int, memo string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"` + memo + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.transaction." + nanoIDFromRequestID(reqID)
}

// debitAt posts a plain DebitAccount charge (no clauseRef, so no recorded
// dueAt — due on receipt).
func debitAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) string {
	t.Helper()
	return postEntryAt(t, ctx, conn, cp, cons, label, "DebitAccount", acctKey, submittedAt, amountCents, "Rent")
}

// creditAt is debitAt's payment counterpart.
func creditAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) string {
	t.Helper()
	return postEntryAt(t, ctx, conn, cp, cons, label, "CreditAccount", acctKey, submittedAt, amountCents, "Rent payment")
}

// seedAspect plants one aspect document directly — the state a prior
// evaluation (or another package) would have left, without running it.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vtxKey+"."+localName, b); err != nil {
		t.Fatalf("seed aspect %s.%s: %v", vtxKey, localName, err)
	}
}

// seedEntryAt seeds one already-posted transaction on acctKey at an EXPLICIT
// postedAt — the vertex, its .entry aspect and the postedTo link back — the
// shape a committed entry sits in, with no op run. A non-empty dueAt is the
// recorded due date a clause-authorized rent charge carries.
func seedEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int, postedAt, dueAt string) {
	t.Helper()
	txKey := "vtx.transaction." + txID
	acctID := acctKey[len("vtx.account."):]
	seedVertex(t, ctx, conn, txKey, "transaction", nil)
	entry := map[string]any{"type": entryType, "amountCents": amountCents, "postedAt": postedAt}
	if dueAt != "" {
		entry["dueAt"] = dueAt
	}
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", entry)
	seedLink(t, ctx, conn,
		"lnk.transaction."+txID+".postedTo.account."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
}

// seedAccountHeldFor seeds an account held for a lease directly — the shape
// LoftspaceCreateAccount commits, without running it.
func seedAccountHeldFor(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, leaseKey string) string {
	t.Helper()
	acctKey := "vtx.account." + acctID
	leaseID := leaseKey[len("vtx.leaseapp."):]
	seedVertex(t, ctx, conn, acctKey, "account", nil)
	seedLink(t, ctx, conn,
		"lnk.account."+acctID+".heldFor.leaseapp."+leaseID,
		acctKey, leaseKey, "heldFor", "heldFor")
	return acctKey
}

// tombstoneVertex rewrites a vertex as tombstoned — the shape
// WithdrawLeaseApplication leaves a leaseapp in, its links untouched.
func tombstoneVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone %s: %v", key, err)
	}
}

// remindFor is a due date plus the package's grace — the same arithmetic the
// tenant's own statement runs, from the package's constant rather than a
// second literal.
func remindFor(t *testing.T, dueAt string) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339, dueAt)
	if err != nil {
		t.Fatalf("parse %q: %v", dueAt, err)
	}
	return at.AddDate(0, 0, loftspaceledger.ArrearsGraceDays).Format(time.RFC3339)
}

// requireOwed asserts the shape an owed-but-not-yet-reminded evaluation
// writes: dueAt, remindAt = dueAt + grace, and none of the send record.
func requireOwed(t *testing.T, data map[string]any, wantDue string) {
	t.Helper()
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q", got, wantDue)
	}
	if got, _ := data["remindAt"].(string); got != remindFor(t, wantDue) {
		t.Fatalf("remindAt = %q, want %q (dueAt + the grace)", got, remindFor(t, wantDue))
	}
}

// TestArrears_FirstChargeMintsNothingAndEvaluationOpensTheEpisode (a). A
// charge against an account with no arrears state mints NOTHING: with no
// stored balance the entry cannot know it is the head, and such an account
// is already opening the never-evaluated gap. The evaluation is what opens
// the episode — it replays the history, finds the charge open, and stamps
// dueAt = that charge's own postedAt (a plain DebitAccount records no due
// date, so it is due on receipt) and remindAt = dueAt + the grace; evaluated
// inside the grace, so no remindedFor, no sentAt, nothing sent.
func TestArrears_FirstChargeMintsNothingAndEvaluationOpensTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsopen")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSQPENLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarropenacct0000001", leaseKey)
	if arrearsData(t, ctx, conn, acctKey) != nil {
		t.Fatal("LoftspaceCreateAccount must mint NO .arrears — a new account owes nothing, and its missing evaluatedAt is what opens the gap once")
	}

	debitAt(t, ctx, conn, cp, cons, "bbarropendebit000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	if data := arrearsData(t, ctx, conn, acctKey); data != nil {
		t.Fatalf("a charge against an account with no arrears state must mint nothing — the never-evaluated gap does the first evaluation: %+v", data)
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarropeneval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("the evaluation must write .arrears — its evaluatedAt is what closes the never-evaluated gap")
	}
	requireOwed(t, data, "2026-09-01T09:00:00Z")
	if got, _ := data["evaluatedAt"].(string); got != "2026-09-03T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the evaluation's own submittedAt", got)
	}
	for _, absent := range []string{"remindedFor", "sentAt", "stale", "historyTooLong"} {
		if _, ok := data[absent]; ok {
			t.Fatalf("a head still inside its grace carries no %s: %+v", absent, data)
		}
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a head still inside its grace must send nothing, got %+v", notif)
	}
}

// TestArrears_RecordedDueDateWinsOverPostedAt is LoftSpace's own rule: the
// head's recorded .entry.dueAt is the fact aged, never its postedAt and never
// a term added to it. A rent charge posted LATE (Sep 3, for rent recorded due
// Sep 1) is overdue from Sep 1, and its grace runs from there — so an
// evaluation on Sep 5 sends nothing, and one at Sep 6 09:00 (exactly dueAt +
// 5 days) stamps remindedFor = the RECORDED due date and sends. A charge
// posted EARLY (Aug 25, for rent due Sep 1) is not overdue at all until Sep 1.
func TestArrears_RecordedDueDateWinsOverPostedAt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrecorded")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSRCDLEASEHJK")
	late := seedAccountHeldFor(t, ctx, conn, "BBARREARSRCDACCTHJKM", leaseKey)
	seedEntryAt(t, ctx, conn, late, "BBARREARSRCDTXNAHJKM", "debit", 240000, "2026-09-03T09:00:00Z", "2026-09-01T09:00:00Z")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrcdeval00000001",
		bootstrap.WeaverIdentityKey, late, "2026-09-05T09:00:00Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, late)
	requireOwed(t, data, "2026-09-01T09:00:00Z")
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("Sep 5 is inside the grace after the recorded Sep 1 due date: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("nothing goes out inside the grace, got %+v", notif)
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrcdeval00000002",
		bootstrap.WeaverIdentityKey, late, "2026-09-06T09:00:00Z", processor.OutcomeAccepted)
	sent := arrearsData(t, ctx, conn, late)
	if got, _ := sent["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the RECORDED due date, not the posting", got)
	}
	if got, _ := sent["sentAt"].(string); got != "2026-09-06T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation at exactly remindAt — the grace bound is inclusive", got)
	}
	notif := arrearsNotification(t, ctx, conn, reqID2)
	if notif == nil {
		t.Fatal("at remindAt the reminder goes out")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["dueAt"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("params.dueAt = %q, want the recorded due date the tenant recognises", got)
	}

	// The early-posted sibling: nothing is overdue before its recorded date.
	earlyLease := seedLease(t, ctx, conn, "BBARREARSRCELEASEHJK")
	early := seedAccountHeldFor(t, ctx, conn, "BBARREARSRCEACCTHJKM", earlyLease)
	seedEntryAt(t, ctx, conn, early, "BBARREARSRCETXNAHJKM", "debit", 240000, "2026-08-25T09:00:00Z", "2026-09-01T09:00:00Z")
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrcdeval00000003",
		bootstrap.WeaverIdentityKey, early, "2026-09-02T09:00:00Z", processor.OutcomeAccepted)
	earlyData := arrearsData(t, ctx, conn, early)
	requireOwed(t, earlyData, "2026-09-01T09:00:00Z")
	if got, _ := earlyData["remindAt"].(string); got != "2026-09-06T09:00:00Z" {
		t.Fatalf("remindAt = %q — the grace runs from the recorded due date, not from the Aug 25 posting", got)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID3); notif != nil {
		t.Fatalf("a charge posted a week before its due date is not overdue on Sep 2: %+v", notif)
	}
}

// TestArrears_HeadWithoutDueDateIsDueOnReceipt: a landlord one-off
// (LoftspaceRecordCharge) records no due date, so the head is due at its own
// postedAt and the grace runs from there. One second before remindAt nothing
// goes out; at remindAt it does — the same inclusive bound the recorded-date
// vector pins, here on the fallback.
func TestArrears_HeadWithoutDueDateIsDueOnReceipt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsreceipt")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSRCPLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrrcpacct00000001", leaseKey)
	postEntryAt(t, ctx, conn, cp, cons, "bbarrrcpcharge000001", "LoftspaceRecordCharge", acctKey, "2026-09-01T09:00:00Z", 4500, "Lock change")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrcpeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-06T08:59:59Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, data, "2026-09-01T09:00:00Z")
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("one second before remindAt is still inside the grace: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("nothing goes out one second before remindAt, got %+v", notif)
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrcpeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-06T09:00:00Z", processor.OutcomeAccepted)
	sent := arrearsData(t, ctx, conn, acctKey)
	if got, _ := sent["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the posting the one-off fell due at", got)
	}
	if arrearsNotification(t, ctx, conn, reqID2) == nil {
		t.Fatal("at remindAt the one-off's reminder goes out")
	}
}

// TestArrears_SecondChargeLeavesTheHead (b). The head is the OLDEST open
// charge, and a second charge queues behind it. This ledger's post_entry
// cannot see that (no balance), so it marks the recorded state stale and
// carries dueAt untouched; the recomputation the stale mark asks for then
// finds the same head and clears the mark — never re-stamping the due date of
// a weeks-old debt to the day of the newest charge.
func TestArrears_SecondChargeLeavesTheHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssecond")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSSNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrsndacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrsnddebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrsndeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsData(t, ctx, conn, acctKey)

	debitAt(t, ctx, conn, cp, cons, "bbarrsnddebit0000002", acctKey, "2026-09-20T09:00:00Z", 4500)

	marked := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := marked["stale"].(bool); !stale {
		t.Fatalf("a posted entry must mark the recorded arrears state stale — it has no balance to reason from: %+v", marked)
	}
	if marked["dueAt"] != first["dueAt"] || marked["remindAt"] != first["remindAt"] {
		t.Fatalf("the stale mark is an ADDITION, not a rewrite: %+v became %+v", first, marked)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarrsndeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-21T09:00:00Z", processor.OutcomeAccepted)
	second := arrearsData(t, ctx, conn, acctKey)
	if second["dueAt"] != first["dueAt"] {
		t.Fatalf("dueAt moved from %v to %v on a second charge — the head is the OLDEST open charge", first["dueAt"], second["dueAt"])
	}
	if _, ok := second["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for, so it must clear it: %+v", second)
	}
}

// TestArrears_PartialPaymentMarksStale (c). A partial payment can move the
// FIFO head to a later charge with a later due date, which no single entry
// can compute — so post_entry marks the recorded state stale rather than
// guessing, and the convergence lens reads stale as an open gap.
func TestArrears_PartialPaymentMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsstale")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSSTLLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrstlacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrstldebit0000001", acctKey, "2026-09-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "bbarrstldebit0000002", acctKey, "2026-09-20T09:00:00Z", 500)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrstleval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-20T10:00:00Z", processor.OutcomeAccepted)
	before := arrearsData(t, ctx, conn, acctKey)
	if _, ok := before["stale"]; ok {
		t.Fatalf("fixture precondition: a fresh evaluation carries no stale mark: %+v", before)
	}

	creditAt(t, ctx, conn, cp, cons, "bbarrstlpay000000001", acctKey, "2026-09-21T09:00:00Z", 1000)

	after := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("a partial payment must mark the recorded arrears state stale, got %+v", after)
	}
	if after["dueAt"] != before["dueAt"] {
		t.Fatalf("the stale mark is an ADDITION, not a rewrite: dueAt moved from %v to %v", before["dueAt"], after["dueAt"])
	}
	if after["evaluatedAt"] != before["evaluatedAt"] {
		t.Fatalf("a posted entry is not an evaluation; evaluatedAt moved from %v to %v", before["evaluatedAt"], after["evaluatedAt"])
	}
}

// TestArrears_PaymentToZeroEndsTheEpisodeOnEvaluation (d). Paying the balance
// off cannot end the episode at the entry — post_entry sees no balance, so it
// marks the reminded state stale and CARRIES the send record (dropping it
// here would let a partial payment that happened to be mis-marked send
// twice). The evaluation the mark asks for finds no open head and rewrites
// .arrears to {evaluatedAt} alone: no remindAt, so no timer stays armed, and
// nothing of the finished episode survives — which is what lets the NEXT
// charge open a clean episode and be reminded for on its own merits.
func TestArrears_PaymentToZeroEndsTheEpisodeOnEvaluation(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscleared")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSCLRLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrclracct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrclrdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrclreval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; !ok {
		t.Fatal("fixture precondition: the head past its grace must have been reminded for")
	}

	creditAt(t, ctx, conn, cp, cons, "bbarrclrpay000000001", acctKey, "2026-09-15T09:00:00Z", 240000)
	marked := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := marked["stale"].(bool); !stale {
		t.Fatalf("a clearing payment cannot be told from a partial one at the entry — it must mark stale: %+v", marked)
	}
	if got, _ := marked["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q — the entry must CARRY the send record; only the evaluation may drop it", got)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarrclreval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-16T09:00:00Z", processor.OutcomeAccepted)
	cleared := arrearsData(t, ctx, conn, acctKey)
	if got, _ := cleared["evaluatedAt"].(string); got != "2026-09-16T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the evaluation's own submittedAt", got)
	}
	for _, absent := range []string{"dueAt", "remindAt", "remindedFor", "sentAt", "stale", "historyTooLong"} {
		if _, ok := cleared[absent]; ok {
			t.Fatalf("a paid-off account keeps no %s — the episode is over and {evaluatedAt} alone remains: %+v", absent, cleared)
		}
	}

	// A NEW charge, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "bbarrclrdebit0000002", acctKey, "2026-09-19T09:00:00Z", 4500)
	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrclreval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-30T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID) == nil {
		t.Fatal("a NEW episode past its grace must send — one per episode is not one per account")
	}
	reopened := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, reopened, "2026-09-19T09:00:00Z")
}

// TestArrears_NewEpisodeBeforeEvaluationDropsTheOldSend (d2) is the episode
// boundary this ledger has to find at evaluation time, because nothing ends
// an episode at the entry: a payment to zero and a fresh charge both post
// before the evaluation runs. The recorded send (Sep 12) was for the Sep 1
// charge, which the Sep 15 payment retired; the Sep 15 charge opens a NEW
// episode, and its head posted after the send. The evaluation must drop the
// finished episode's remindedFor/sentAt — carrying them would leave the new
// charge looking already reminded, and when its grace ran out remindedFor
// would be stamped with no notification ever going out for it.
func TestArrears_NewEpisodeBeforeEvaluationDropsTheOldSend(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsboundary")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSBNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrbndacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrbnddebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrbndeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if got, _ := arrearsData(t, ctx, conn, acctKey)["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("fixture precondition: the Sep 1 charge must have been reminded for, sentAt = %q", got)
	}

	// Paid off, then charged again, with no evaluation in between.
	creditAt(t, ctx, conn, cp, cons, "bbarrbndpay000000001", acctKey, "2026-09-15T09:00:00Z", 240000)
	debitAt(t, ctx, conn, cp, cons, "bbarrbnddebit0000002", acctKey, "2026-09-15T09:05:00Z", 4500)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrbndeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-15T09:10:00Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, data, "2026-09-15T09:05:00Z")
	if _, ok := data["sentAt"]; ok {
		t.Fatalf("the finished episode's send record must not survive into the new one: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("remindedFor travels with sentAt and must go with it: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("the new head is inside its grace, so nothing goes out: %+v", notif)
	}

	// The new episode's grace runs out and it is reminded for on its own merits.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrbndeval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-30T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID2) == nil {
		t.Fatal("the new episode past its grace must send — the old episode's send does not count for it")
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if got, _ := after["sentAt"].(string); got != "2026-09-30T09:00:00Z" {
		t.Fatalf("sentAt = %q, want this evaluation's own instant", got)
	}
	if got, _ := after["remindedFor"].(string); got != "2026-09-15T09:05:00Z" {
		t.Fatalf("remindedFor = %q, want the new head's due date", got)
	}
}

// TestArrears_SendInTheOpenersOwnSecondIsThisEpisodes is the tie rule, and
// it goes the other way from the wellness ledger's: a send stamped in the
// very second the episode's opener posted belongs to THIS episode. A rent
// charge posted five or more days after its recorded due date is past its
// grace the moment it lands, so the never-evaluated gap can evaluate it —
// and send — in the second it posted, leaving sentAt == episodeStart. The
// next evaluation must read that as its own send and send nothing again; a
// strict-before compare is what keeps it. (An earlier episode's send at the
// identical second would need the send, the clearing payment and the new
// charge all inside one second.)
func TestArrears_SendInTheOpenersOwnSecondIsThisEpisodes(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearstie")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSTQELEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSTQEACCTHJKM", leaseKey)
	// Recorded due Sep 1, posted Sep 12 09:00:00 — already six days past its
	// grace when it lands.
	seedEntryAt(t, ctx, conn, acctKey, "BBARREARSTQETXNAHJKM", "debit", 240000, "2026-09-12T09:00:00Z", "2026-09-01T09:00:00Z")

	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "bbarrtieeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: a head past its grace sends in the second it is first evaluated")
	}
	sent := arrearsData(t, ctx, conn, acctKey)
	if got, _ := sent["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("fixture precondition: sentAt = %q, want the opener's own posting second", got)
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrtieeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-13T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a send in the opener's own second is this episode's — nothing goes out again: %+v", notif)
	}
	again := arrearsData(t, ctx, conn, acctKey)
	if got, _ := again["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the tie kept as this episode's own send", got)
	}
	if got, _ := again["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the recorded due date carried", got)
	}
}

// TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode is the moved-head
// boundary: A (due on receipt Sep 1) is reminded for on Sep 6; B posts Oct 1;
// on Oct 3 the tenant pays exactly A. The head moves to B, which posted AFTER
// the recorded send — but the account was never square, so this is the same
// episode and the send record must be carried. A boundary read off the head's
// own postedAt would drop it here and send again on Oct 6 for a debt the
// tenant is visibly paying down.
func TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsmovedhead")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSMVHLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrmvhacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrmvhdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "bbarrmvheval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-06T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: A is reminded for at its remindAt")
	}

	debitAt(t, ctx, conn, cp, cons, "bbarrmvhdebit0000002", acctKey, "2026-10-01T09:00:00Z", 240000)
	creditAt(t, ctx, conn, cp, cons, "bbarrmvhpay000000001", acctKey, "2026-10-03T09:00:00Z", 240000)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrmvheval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-10-03T10:00:00Z", processor.OutcomeAccepted)
	moved := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, moved, "2026-10-01T09:00:00Z")
	if got, _ := moved["sentAt"].(string); got != "2026-09-06T09:00:00Z" {
		t.Fatalf("sentAt = %q — the head moved but the account was never square, so the episode's send is carried", got)
	}
	if got, _ := moved["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want A's due date carried (B is inside its grace)", got)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("B is inside its grace: %+v", notif)
	}

	// B's grace runs out. remindedFor moves to B's due date, and NOTHING is
	// sent — one reminder per episode, and this episode already had it.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrmvheval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-10-07T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("a second reminder for one continuous episode: %+v", notif)
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if got, _ := after["remindedFor"].(string); got != "2026-10-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want B's due date — it is what closes the gap", got)
	}
	if got, _ := after["sentAt"].(string); got != "2026-09-06T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the episode's one send carried", got)
	}
}

// TestArrears_ExactRetirementAcrossPageBoundaryKeepsSentAt proves
// arrears_head's EPISODE START tracking — not just its head/remindAt — is
// exact ACROSS pages, the reason the checkpoint keeps every entry's own
// postedAt instead of collapsing credits into a running total (see
// arrears_rows' doc comment). It is the paged form of
// TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode: charge A is
// reminded for, charge B posts later and queues behind it, and a PLAIN
// credit — this ledger has no reverses relation at all — exactly retires A,
// moving the head to B while the account was never square in between. Here
// the retiring credit sits alone on page 2 and A sits on page 1, so the
// finalize walk only sees both in the same chronological order a
// whole-history execution would if (and only if) the checkpoint preserved
// each entry's own postedAt.
func TestArrears_ExactRetirementAcrossPageBoundaryKeepsSentAt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsxpageretire")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSXRTLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSXRTACCTHJKM", leaseKey)

	chargeA := replayTxID('A', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeA, "debit", 240000, "2026-08-01T09:00:00Z", "")

	// 14 filler debit/credit pairs, all dated well before A and netting to
	// zero exactly — each pair opens and immediately re-empties the queue
	// long before A ever arrives, so none of them touches episode_start.
	// Present purely to fill page 1 up to the limit, so the entries that
	// matter land on the pages this vector is named for.
	for i := 0; i < 14; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('F', i), "debit", 500, "2026-01-01T00:00:00Z", "")
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('F', i+14), "credit", 500, "2026-01-01T00:00:00Z", "")
	}

	// One page (1 + 28 = 29 entries): the pre-existing episode is reminded
	// for, BEFORE B or the retiring credit exist.
	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "bbarrxrteval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-10T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: A is reminded for past its remindAt")
	}
	if arrearsReplay(t, arrearsData(t, ctx, conn, acctKey)) != nil {
		t.Fatal("fixture precondition: 29 entries fit in one page")
	}

	// chargeB sorts onto page 1's 30th slot; the credit that exactly
	// retires A sorts alone onto page 2 — the shape the vector is named for.
	chargeB := replayTxID('Y', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeB, "debit", 240000, "2026-09-01T09:00:00Z", "")
	payoffA := replayTxID('z', 0)
	seedEntryAt(t, ctx, conn, acctKey, payoffA, "credit", 240000, "2026-09-03T09:00:00Z", "")

	// Dispatch 1: page 1 (30 entries: A, the 28 fillers, B) — mid-replay.
	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrxrteval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T10:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, req1) != nil {
		t.Fatal("a page sends nothing")
	}
	mid := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, mid) == nil {
		t.Fatalf("31 entries must leave a checkpoint after page 1: %+v", mid)
	}

	// Dispatch 2: page 2 (the lone retiring credit) — finalizes.
	_, req2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrxrteval0000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T11:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("two pages, two dispatches: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("dueAt = %q, want %q — the exact retirement (split across the page boundary) moves the head to B", got, "2026-09-01T09:00:00Z")
	}
	if got, _ := final["remindAt"].(string); got != remindFor(t, "2026-09-01T09:00:00Z") {
		t.Fatalf("remindAt = %q, want %q — B's own due date plus the grace", got, remindFor(t, "2026-09-01T09:00:00Z"))
	}
	if got, _ := final["sentAt"].(string); got != "2026-08-10T09:00:00Z" {
		t.Fatalf("sentAt = %q — the head moved but the account was never square, so the episode's send (from BEFORE the page split) must be carried, not dropped as if B had opened a fresh episode", got)
	}
	if got, _ := final["remindedFor"].(string); got != "2026-08-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want A's due date carried (B is not yet due)", got)
	}
	if notif := arrearsNotification(t, ctx, conn, req2); notif != nil {
		t.Fatalf("B is not yet due, and the episode was already reminded for: %+v", notif)
	}
}

// TestArrears_RepeatedDueDateMintsADistinctEpisodeKey pins the notification
// key's uniqueness. Recorded due dates repeat across charges on one lease (a
// second clause on the same anniversary grid; a backfilled first period), so
// two episodes reminded for the SAME recorded dueAt must carry DIFFERENT
// episode keys — the adapter dedups on the key, and a key of the account and
// the date alone would swallow the second episode's reminder while sentAt
// recorded it as sent. The head transaction's own key is what tells them
// apart, and the bridge's reply for each still lands remindedFor = dueAt.
func TestArrears_RepeatedDueDateMintsADistinctEpisodeKey(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrepeatdue")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSRPTLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSRPTACCTHJKM", leaseKey)
	const dueAt = "2026-09-01T00:00:00Z"
	seedEntryAt(t, ctx, conn, acctKey, "BBARREARSRPTTXNAHJKM", "debit", 240000, "2026-09-01T09:00:00Z", dueAt)

	_, reqA := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrpteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-10T09:00:00Z", processor.OutcomeAccepted)
	notifA := arrearsNotification(t, ctx, conn, reqA)
	if notifA == nil {
		t.Fatal("the first episode past its grace sends")
	}
	refA, _ := notifA["externalRef"].(string)
	if refA != acctKey+":"+dueAt+":vtx.transaction.BBARREARSRPTTXNAHJKM" {
		t.Fatalf("externalRef = %q, want <accountKey>:<dueAt>:<headTransactionKey>", refA)
	}

	// Paid off; the episode ends on evaluation.
	creditAt(t, ctx, conn, cp, cons, "bbarrrptpay000000001", acctKey, "2026-09-11T09:00:00Z", 240000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrrpteval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; ok {
		t.Fatal("fixture precondition: the paid-off episode keeps no send record")
	}

	// A second charge recorded due on the SAME date — a backfilled period on
	// another clause — opens a new episode already past its grace.
	seedEntryAt(t, ctx, conn, acctKey, "BBARREARSRPTTXNBHJKM", "debit", 120000, "2026-09-13T09:00:00Z", dueAt)
	_, reqB := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrpteval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	notifB := arrearsNotification(t, ctx, conn, reqB)
	if notifB == nil {
		t.Fatal("the second episode past its grace sends too")
	}
	refB, _ := notifB["externalRef"].(string)
	if refB == refA {
		t.Fatalf("both episodes carry the episode key %q — the adapter would dedup the second reminder away", refB)
	}
	if refB != acctKey+":"+dueAt+":vtx.transaction.BBARREARSRPTTXNBHJKM" {
		t.Fatalf("externalRef = %q, want the second head's own key in the token", refB)
	}
	for _, notif := range []map[string]any{notifA, notifB} {
		if got, _ := notif["instanceKey"].(string); got != notif["externalRef"] {
			t.Fatalf("instanceKey = %q, want it equal to the externalRef %v", got, notif["externalRef"])
		}
		if got, _ := notif["idempotencyKey"].(string); got != notif["externalRef"] {
			t.Fatalf("idempotencyKey = %q, want it equal to the externalRef %v", got, notif["externalRef"])
		}
	}

	// The bridge's reply for each token recovers the same recorded due date.
	recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrrptreplya000001", refA, "completed", "2026-09-10T09:00:05Z", processor.OutcomeAccepted)
	if got, _ := arrearsNotificationOutcome(t, ctx, conn, acctKey)["remindedFor"].(string); got != dueAt {
		t.Fatalf("reply A: remindedFor = %q, want %q parsed out of the three-part token", got, dueAt)
	}
	recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrrptreplyb000001", refB, "completed", "2026-09-14T09:00:05Z", processor.OutcomeAccepted)
	second := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := second["remindedFor"].(string); got != dueAt {
		t.Fatalf("reply B: remindedFor = %q, want %q", got, dueAt)
	}
	if got, _ := second["sentAt"].(string); got != "2026-09-14T09:00:05Z" {
		t.Fatalf("reply B: sentAt = %q, want the later reply's own instant (the aspect holds the latest episode)", got)
	}
}

// TestArrears_PartialPaymentThenChargeKeepsTheSend is the boundary vector's
// positive sibling: the same sequence with a payment that leaves the balance
// open. The head stays the Sep 1 charge (partially paid), the episode never
// ended, and the evaluation must CARRY the send record — the tenant is still
// in the debt they were reminded of, and dropping the record would send
// again for it.
func TestArrears_PartialPaymentThenChargeKeepsTheSend(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscontinues")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSCNTLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrcntacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrcntdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrcnteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)

	creditAt(t, ctx, conn, cp, cons, "bbarrcntpay000000001", acctKey, "2026-09-15T09:00:00Z", 100000)
	debitAt(t, ctx, conn, cp, cons, "bbarrcntdebit0000002", acctKey, "2026-09-15T09:05:00Z", 4500)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrcnteval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-15T09:10:00Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, data, "2026-09-01T09:00:00Z")
	if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q — the episode continues, so its send record is carried", got)
	}
	if got, _ := data["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the head's due date carried", got)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("an episode already reminded for sends nothing on a re-evaluation: %+v", notif)
	}
}

// TestArrears_EvaluateSendsOnceThenNothing (e) is the green bar: a tenant
// past the grace is reminded ONCE per episode. The first evaluation stamps
// remindedFor + sentAt and emits the external.notification the bridge turns
// into a real message — addressed through the account's live lease to the
// tenant it is applicationFor; a re-dispatch recomputes the same head, finds
// sentAt already recorded, and emits nothing at all.
func TestArrears_EvaluateSendsOnceThenNothing(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssend")

	tenantKey := seedIdentity(t, ctx, conn, "BBARREARSSENDTNTHJKM")
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBARREARSSENDLEASEHJ", "BBARREARSSENDTNTHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrsendacct0000001", leaseKey)
	headKey := debitAt(t, ctx, conn, cp, cons, "bbarrsenddebit000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	wantDue := "2026-09-01T09:00:00Z"

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrsendeval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, data, wantDue)
	if got, _ := data["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q — this is what closes the gap for the episode", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a head past its grace must emit external.notification — the send is the whole point of the target")
	}
	wantRef := acctKey + ":" + wantDue + ":" + headKey
	if got, _ := notif["externalRef"].(string); got != wantRef {
		t.Fatalf("externalRef = %q, want %q (the episode key the adapter dedups on: account, recorded due date, head transaction)", got, wantRef)
	}
	if got, _ := notif["instanceKey"].(string); got != wantRef {
		t.Fatalf("instanceKey = %q, want %q", got, wantRef)
	}
	if got, _ := notif["idempotencyKey"].(string); got != wantRef {
		t.Fatalf("idempotencyKey = %q, want %q", got, wantRef)
	}
	if got, _ := notif["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := notif["replyOp"].(string); got != "RecordLoftspaceArrearsReminderNotification" {
		t.Fatalf("replyOp = %q, want RecordLoftspaceArrearsReminderNotification", got)
	}
	params, _ := notif["params"].(map[string]any)
	if params == nil {
		t.Fatalf("the notification carries no params: %+v", notif)
	}
	if got, _ := params["accountKey"].(string); got != acctKey {
		t.Fatalf("params.accountKey = %q, want %q", got, acctKey)
	}
	if got, _ := params["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("params.leaseAppKey = %q, want %q (resolved live off the account's own heldFor link)", got, leaseKey)
	}
	if got, _ := params["identityKey"].(string); got != tenantKey {
		t.Fatalf("params.identityKey = %q, want %q (resolved live off the lease's own applicationFor link, not the payload)", got, tenantKey)
	}
	if got, _ := params["reminderType"].(string); got != "loftspaceRentArrears" {
		t.Fatalf("params.reminderType = %q, want loftspaceRentArrears", got)
	}
	if got, _ := params["dueAt"].(string); got != wantDue {
		t.Fatalf("params.dueAt = %q, want %q", got, wantDue)
	}
	if got, _ := params["balanceCents"].(float64); got != 240000 {
		t.Fatalf("params.balanceCents = %v, want 240000", got)
	}

	// The re-dispatch. Same account, same history, a later instant: the head
	// is unchanged, sentAt already stands, and NOTHING goes out.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrsendeval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-13T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("a re-dispatched evaluation must send NOTHING for an episode already reminded for, got %+v", notif)
	}
	again := arrearsData(t, ctx, conn, acctKey)
	if got, _ := again["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt was re-stamped to %q — the original send record must be carried forward, not rewritten", got)
	}
	if got, _ := again["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q carried forward", got, wantDue)
	}
}

// TestArrears_EvaluateRearmsOnACoveredHead (f). A payment covered the charge
// the recorded due date came from, so the FIFO head is now a LATER charge
// with a later due date. The evaluation recomputes it, clears stale, carries
// no send record (none was made), and the recomputed reminder instant re-arms
// the timer — with no notification, because nothing is past its grace yet.
func TestArrears_EvaluateRearmsOnACoveredHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrearm")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSRRMLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrrearmacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrrearmdebit00001", acctKey, "2026-09-01T09:00:00Z", 1000)
	// The episode opens on the Sep 1 charge (reminder armed Sep 6), evaluated
	// inside its grace so nothing is reminded for yet.
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrrearmeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T09:00:00Z", processor.OutcomeAccepted)
	debitAt(t, ctx, conn, cp, cons, "bbarrrearmdebit00002", acctKey, "2026-09-20T09:00:00Z", 500)
	creditAt(t, ctx, conn, cp, cons, "bbarrrearmpay0000001", acctKey, "2026-09-21T09:00:00Z", 1000)
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the partial payment must have marked the state stale")
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrrearmeval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	// The Sep 1 charge was fully paid off, so the head is the Sep 20 one.
	requireOwed(t, data, "2026-09-20T09:00:00Z")
	if _, ok := data["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for, so it must clear it: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("nothing has been reminded for this episode: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a head inside its grace must send nothing, got %+v", notif)
	}
}

// TestArrears_OneNotificationPerEpisodeNotPerHead is the once-per-EPISODE
// guarantee under a partial payment that moves the head: two charges, the
// first reminded for; a payment retires it exactly, so the head moves to the
// second — whose own grace has ALSO run out — and nothing may go out for it.
// A design that keyed the send off remindedFor alone would nag here. Then the
// balance clears, the episode ends, and the NEXT charge sends again.
func TestArrears_OneNotificationPerEpisodeNotPerHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsepisode")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSEPSLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrepsacct00000001", leaseKey)

	debitAt(t, ctx, conn, cp, cons, "bbarrepsdebit0000001", acctKey, "2026-09-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "bbarrepsdebit0000002", acctKey, "2026-09-05T09:00:00Z", 1000)

	// The first charge's grace runs out and the reminder goes out.
	_, reqID1 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrepseval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID1) == nil {
		t.Fatal("the first head past its grace in an episode must send")
	}
	sent := arrearsData(t, ctx, conn, acctKey)
	if got, _ := sent["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the first head's due date", got)
	}
	if got, _ := sent["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	// A PARTIAL payment retires the first charge exactly. The head moves to
	// the second, whose own grace has also run out — and nothing may go out
	// for it.
	creditAt(t, ctx, conn, cp, cons, "bbarrepspay000000001", acctKey, "2026-09-13T09:00:00Z", 1000)
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: a posted entry marks the state stale")
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrepseval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("the head moved within ONE episode — a second notification is the nag the design forbids: %+v", notif)
	}
	moved := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, moved, "2026-09-05T09:00:00Z")
	if got, _ := moved["remindedFor"].(string); got != "2026-09-05T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the moved head's due date — it is what closes the gap", got)
	}
	if got, _ := moved["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the episode's original send carried", got)
	}

	// The balance clears; the evaluation ends the episode.
	creditAt(t, ctx, conn, cp, cons, "bbarrepspay000000002", acctKey, "2026-09-16T09:00:00Z", 1000)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrepseval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-17T09:00:00Z", processor.OutcomeAccepted)
	cleared := arrearsData(t, ctx, conn, acctKey)
	if _, ok := cleared["sentAt"]; ok {
		t.Fatalf("a paid-off account keeps no send record — the NEXT episode must be able to send: %+v", cleared)
	}
	if _, ok := cleared["dueAt"]; ok {
		t.Fatalf("a paid-off account carries no dueAt: %+v", cleared)
	}

	// A NEW charge, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "bbarrepsdebit0000003", acctKey, "2026-09-19T09:00:00Z", 500)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; ok {
		t.Fatal("a fresh episode starts with no send record")
	}
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrepseval00000004",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-30T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID3) == nil {
		t.Fatal("a NEW episode past its grace must send — one per episode is not one per account")
	}
}

// TestArrears_ForgedSendRefused (g) is the actor guard. ledgerActorKey holds
// the operator role AND the identical Scope:"any" EvaluateLoftspaceArrears
// grant, so step 3 authorizes it; only `op.actor != primordialActor["weaver"]`
// stops it from having the platform tell an arbitrary tenant they owe rent.
// Nothing is written either — a marker minted on a forged send would close
// the gap and suppress the real reminder. A payload carrying sentAt /
// remindedFor is the same forgery through a different door: the op reads
// neither field, so nothing of them reaches the aspect.
func TestArrears_ForgedSendRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsforged")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSFGDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrfgdacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrfgddebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "bbarrfgdeval00000001",
		ledgerActorKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	if data := arrearsData(t, ctx, conn, acctKey); data != nil {
		t.Fatalf("a refused evaluation must write nothing at all: %+v", data)
	}

	// The payload door: a Weaver-actor submission whose payload carries a
	// send record. The op takes accountKey alone; sentAt and remindedFor come
	// from the account's own hydrated state, so the extra fields change
	// nothing about what is stamped — the head is inside its grace at this
	// instant, so neither field may appear.
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("bbarrfgdeval00000002"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateLoftspaceArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-09-03T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","sentAt":"2026-09-02T09:00:00Z","remindedFor":"2026-09-01T09:00:00Z"}`),
		ContextHint:   arrearsHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	if _, ok := data["sentAt"]; ok {
		t.Fatalf("a payload sentAt must never reach the aspect — the send record is the op's own, never the submitter's: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("a payload remindedFor must never reach the aspect: %+v", data)
	}
}

// TestArrears_TenantResolvedFromAccountState (g2a). Neither leaseAppKey nor
// identityKey ever travels through the payload — a Weaver Params reference
// to an optional column would make the strategist refuse to dispatch any row
// where the hop misses, silently starving the very accounts most worth aging.
// The op resolves both itself, live, off the account's own heldFor out-link
// and the lease's own applicationFor out-link, so it decides this with no
// payload field to trust or forge; a payload identityKey naming a stranger is
// ignored.
func TestArrears_TenantResolvedFromAccountState(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsidstate")

	tenantKey := seedIdentity(t, ctx, conn, "BBARREARSQDSTNTHJKMN")
	stranger := seedIdentity(t, ctx, conn, "BBARREARSSTRANGERHJK")
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBARREARSQDSLEASEHJK", "BBARREARSQDSTNTHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarridsacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarridsdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)

	reqID := testutil.GenReqID("bbarridseval00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateLoftspaceArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-09-12T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","identityKey":"` + stranger + `","leaseAppKey":"vtx.leaseapp.BBARREARSFRGDLEASEHJ"}`),
		ContextHint:   arrearsHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-grace account must send")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["identityKey"].(string); got != tenantKey {
		t.Fatalf("params.identityKey = %q, want %q — resolved live off the lease's own applicationFor link, never the payload", got, tenantKey)
	}
	if got, _ := params["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("params.leaseAppKey = %q, want %q — resolved live off the account's own heldFor link, never the payload", got, leaseKey)
	}
}

// TestArrears_WithdrawnLeaseStillEvaluates (g2b) is the other half: an
// account whose lease was withdrawn — WithdrawLeaseApplication tombstones the
// leaseapp and leaves its links dangling live — is still evaluated, because
// the arrears fact is about the ACCOUNT, not the tenant it happens to be held
// for. Were the lease or the identity a Params column this shape would be
// UNREACHABLE: Weaver's strategist refuses to dispatch any row whose Params
// reference a null column, so this account's gap would simply never open. A
// past-grace evaluation on it still sends — with neither leaseAppKey nor
// identityKey in the notification's params, because the walk may not transit
// a dead lease to the applicant still linked off it. An account with no
// heldFor link at all is the same shape one hop earlier.
func TestArrears_WithdrawnLeaseStillEvaluates(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnolease")

	seedIdentity(t, ctx, conn, "BBARREARSWDRTNTHJKMN")
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBARREARSWDRLEASEHJK", "BBARREARSWDRTNTHJKMN")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSWDRACCTHJKM", leaseKey)
	seedEntryAt(t, ctx, conn, acctKey, "BBARREARSWDRTXNAHJKM", "debit", 240000, "2026-06-01T08:00:00Z", "")
	tombstoneVertex(t, ctx, conn, leaseKey, "leaseapp")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrwdreval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	requireOwed(t, data, "2026-06-01T08:00:00Z")
	if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt — a lease-less account still gets its one reminder", got)
	}
	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-grace account with a withdrawn lease must still send")
	}
	params, _ := notif["params"].(map[string]any)
	if _, ok := params["identityKey"]; ok {
		t.Fatalf("the applicant is reached only through the withdrawn lease, so params must carry no identityKey: %+v", params)
	}
	if _, ok := params["leaseAppKey"]; ok {
		t.Fatalf("a withdrawn lease is not a live lease to name: %+v", params)
	}

	// No heldFor at all.
	orphan := "vtx.account.BBARREARSNLSACCTHJKM"
	seedVertex(t, ctx, conn, orphan, "account", nil)
	seedEntryAt(t, ctx, conn, orphan, "BBARREARSNLSTXNAHJKM", "debit", 1500, "2026-06-01T08:00:00Z", "")
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrnlseval00000001",
		bootstrap.WeaverIdentityKey, orphan, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	notif2 := arrearsNotification(t, ctx, conn, reqID2)
	if notif2 == nil {
		t.Fatal("a past-grace account with no lease at all must still send")
	}
	params2, _ := notif2["params"].(map[string]any)
	if _, ok := params2["leaseAppKey"]; ok {
		t.Fatalf("no lease resolved, so params must carry no leaseAppKey: %+v", params2)
	}
	if _, ok := params2["identityKey"]; ok {
		t.Fatalf("no lease resolved, so params must carry no identityKey: %+v", params2)
	}
}

// budgetTxID mints the i-th of the 501 transaction ids the budget vector
// seeds — valid 20-char NanoIDs, distinct per i.
func budgetTxID(i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return "BBARREARSBUDGETTX" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
}

// TestArrears_HistoryPastTheBudgetDegrades (k) is the exhaustion path, and the
// claim is that it is a DEGRADE and not a stop. The replay is paged — one
// kv.Links page of ArrearsPageLimit entries per dispatch, the running
// aggregate checkpointed on the account between pages — and capped at
// ArrearsMaxPages pages, so an account can genuinely outrun it, and the op
// then cannot name a head. A refusal there would be permanent and SILENT: the
// only thing that re-drives this op is the convergence gap the account's own
// row opens, and a rejected op never closes it, so Weaver would re-dispatch the
// same doomed evaluation on every window — no reminder, no error anyone reads,
// forever.
//
// So the op records the exhaustion instead, on the dispatch AFTER the last
// in-budget page. It is ACCEPTED, it sends nothing, it leaves everything
// already recorded untouched (a reminder already sent stays recorded as sent;
// a due date already armed is not erased by an evaluation that could not read
// the history), it records the budget it exhausted, and it drops stale and the
// checkpoint — which the lens pin TestLoftspaceArrears_HistoryTooLongGoesQuiet
// turns into silence. Every page before it carried the episode record and sent
// nothing too. The second half is the way back out: the next posted entry
// drops the flag and its budget and re-marks the state stale, which re-opens
// the gap for exactly one more attempt.
func TestArrears_HistoryPastTheBudgetDegrades(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbudget")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSBGTLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSBGTACCTHJKM", leaseKey)

	// One more posted entry than the budget, so the last in-budget page ends
	// with a live cursor and the budget genuinely runs out.
	overBudget := loftspaceledger.ArrearsPageLimit*loftspaceledger.ArrearsMaxPages + 1
	for i := 0; i < overBudget; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-06-01T08:00:00Z", "")
	}

	// The state a previous, in-budget evaluation left: an episode reminded for.
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"dueAt":       "2026-08-01T09:00:00Z",
		"remindAt":    "2026-08-06T09:00:00Z",
		"remindedFor": "2026-08-01T09:00:00Z",
		"sentAt":      "2026-08-07T09:00:00Z",
		"evaluatedAt": "2026-08-07T09:00:00Z",
		"stale":       true,
	})
	carried := map[string]string{
		"dueAt":       "2026-08-01T09:00:00Z",
		"remindAt":    "2026-08-06T09:00:00Z",
		"remindedFor": "2026-08-01T09:00:00Z",
		"sentAt":      "2026-08-07T09:00:00Z",
	}

	// Drive one dispatch per page until the degrade lands; every page before
	// it is a checkpoint that carries the record and sends nothing.
	dispatches := 0
	var data map[string]any
	for dispatches < loftspaceledger.ArrearsMaxPages+2 {
		dispatches++
		_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrbgteval"+strconv.Itoa(10000000+dispatches),
			bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
		if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
			t.Fatalf("dispatch %d: an evaluation that has not read the whole history must send nothing — it does not know the head: %+v", dispatches, notif)
		}
		data = arrearsData(t, ctx, conn, acctKey)
		for field, want := range carried {
			if got, _ := data[field].(string); got != want {
				t.Fatalf("dispatch %d: %s = %q, want %q carried untouched — a page that read nothing whole must erase nothing", dispatches, field, got, want)
			}
		}
		if flag, _ := data["historyTooLong"].(bool); flag {
			break
		}
		replay, _ := data["replay"].(map[string]any)
		if replay == nil {
			t.Fatalf("dispatch %d: neither a checkpoint nor the degrade: %+v", dispatches, data)
		}
		if got, _ := replay["pages"].(float64); int(got) != dispatches {
			t.Fatalf("dispatch %d: replay.pages = %v, want %d — one page per dispatch", dispatches, replay["pages"], dispatches)
		}
		if got, _ := data["evaluatedAt"].(string); got != "2026-08-07T09:00:00Z" {
			t.Fatalf("dispatch %d: evaluatedAt = %q — a page is not an evaluation, so the last completed one's stamp stands", dispatches, got)
		}
	}
	if dispatches != loftspaceledger.ArrearsMaxPages+1 {
		t.Fatalf("the degrade landed on dispatch %d, want %d — every in-budget page is consumed first, then one more dispatch records the exhaustion", dispatches, loftspaceledger.ArrearsMaxPages+1)
	}
	if flag, _ := data["historyTooLong"].(bool); !flag {
		t.Fatalf("the exhaustion must be RECORDED, not raised — a refusal is a permanent silent stop: %+v", data)
	}
	if got, _ := data["historyBudget"].(float64); int(got) != loftspaceledger.ArrearsPageLimit*loftspaceledger.ArrearsMaxPages {
		t.Fatalf("historyBudget = %v, want %d — the budget the flag was recorded under, so a later larger one can tell it apart", data["historyBudget"], loftspaceledger.ArrearsPageLimit*loftspaceledger.ArrearsMaxPages)
	}
	if _, ok := data["replay"]; ok {
		t.Fatalf("the degrade ends the replay, so the checkpoint must not survive it — a surviving phase would hold a gap open under the flag: %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("stale asks for a recomputation this op has just attempted; re-asking re-opens the gap the degrade closes: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the degrading evaluation's own submittedAt", got)
	}

	// The way back out: one more posted entry, one more attempt.
	debitAt(t, ctx, conn, cp, cons, "bbarrbgtdebit0000001", acctKey, "2026-09-15T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if _, ok := after["historyTooLong"]; ok {
		t.Fatalf("a posted entry must drop the flag, or the row stays quiet for the life of the account: %+v", after)
	}
	if _, ok := after["historyBudget"]; ok {
		t.Fatalf("and the budget recorded beside it — a budget with no flag records nothing: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and re-mark the state stale, which is what re-opens the gap for that one attempt: %+v", after)
	}
	if got, _ := after["sentAt"].(string); got != "2026-08-07T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record is still carried across the re-arming entry", got)
	}
}

// replayTxID encodes (prefix, i) as a valid 20-char NanoID whose first
// character is `lead`, so a fixture can place an entry on a chosen page: the
// postedTo enumeration pages the SORTED link keys, and the transaction id is
// the first varying segment of lnk.transaction.<id>.postedTo…, so ids
// beginning 'A' precede every id beginning 'z'. i is encoded in the last three
// characters over an alphabet that sorts in index order for i < 48.
func replayTxID(lead byte, i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return string(lead) + "LFREPLAYTXAHJKMN" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
}

// arrearsReplay reads the checkpoint off the account's .arrears, or nil when
// none is recorded.
func arrearsReplay(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	replay, _ := data["replay"].(map[string]any)
	return replay
}

// TestArrears_TwoPageHistoryCompletesInTwoDispatches is the resumable
// replay's green bar: a history one entry longer than a page is evaluated
// EXACTLY across two dispatches. The first consumes one page and records a
// checkpoint — phase a, one page, a live cursor, the running aggregate — and
// nothing else: no notification, no evaluatedAt (this account has never been
// evaluated, and a page is not an evaluation). The second exhausts the
// enumeration, drops the checkpoint, computes the FIFO head from the
// aggregate and, the head being overdue, sends once. The head is the one a
// single-execution replay over the same entries names: twenty-five charges a
// day apart and six payments of one charge each — all posted BEFORE any
// charge, so they act as surplus the FIFO applies oldest-first — pay off the
// six oldest charges wherever they sat in the walk, so the head is the
// seventh charge.
func TestArrears_TwoPageHistoryCompletesInTwoDispatches(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearstwopage")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSTW2LEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSTW2ACCTHJKM", leaseKey)

	// 31 entries in id (= page) order: charges at i = 0..24, payments at the
	// six slots 3, 9, 15, 21, 27, 30 — the last of them on the second page.
	const pageLimit = loftspaceledger.ArrearsPageLimit
	total := pageLimit + 1
	paymentSlots := map[int]bool{3: true, 9: true, 15: true, 21: true, 27: true, 30: true}
	day := 0
	wantHead := ""
	for i := 0; i < total; i++ {
		if paymentSlots[i] {
			seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "credit", 100, "2026-05-01T09:00:00Z", "")
			continue
		}
		postedAt := time.Date(2026, 5, 1+day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		if day == 6 {
			wantHead = postedAt
		}
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, postedAt, "")
		day++
	}
	if day != 25 || wantHead == "" {
		t.Fatalf("fixture: %d charges seeded, want 25 with the seventh as the head", day)
	}

	// Dispatch 1: one page, a checkpoint, nothing else.
	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrtwoeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, req1); notif != nil {
		t.Fatalf("a page is not an evaluation — nothing may be sent before the head is known: %+v", notif)
	}
	first := arrearsData(t, ctx, conn, acctKey)
	replay := arrearsReplay(t, first)
	if replay == nil {
		t.Fatalf("a history longer than one page must leave a checkpoint: %+v", first)
	}
	if got, _ := replay["phase"].(string); got != loftspaceledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q — the first page opens missing_replay_a", got, loftspaceledger.ArrearsPhaseA)
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1", replay["pages"])
	}
	if got, _ := replay["cursor"].(string); got == "" {
		t.Fatalf("replay.cursor must be the live cursor the next page resumes from: %+v", replay)
	}
	if _, ok := first["evaluatedAt"]; ok {
		t.Fatalf("this account has never been evaluated and a page is not an evaluation, so no evaluatedAt may be stamped: %+v", first)
	}
	if _, ok := first["dueAt"]; ok {
		t.Fatalf("no head is known mid-replay: %+v", first)
	}
	entries, _ := replay["entries"].(map[string]any)
	if len(entries) != pageLimit {
		t.Fatalf("replay.entries carries %d rows after the first page, want %d (every entry the page read, debits and credits alike)", len(entries), pageLimit)
	}

	// Dispatch 2: the enumeration is exhausted, the head is computed and sent for.
	_, req2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrtwoeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the finalize page writes no checkpoint: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != wantHead {
		t.Fatalf("dueAt = %q, want %q — six payments retire the six oldest charges whatever page they sat on, so the seventh charge is the head", got, wantHead)
	}
	if got, _ := final["evaluatedAt"].(string); got != "2026-08-23T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the finalizing dispatch's own submittedAt", got)
	}
	if got, _ := final["sentAt"].(string); got != "2026-08-23T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the finalizing dispatch's own submittedAt — the head is overdue and nothing has gone out", got)
	}
	notif := arrearsNotification(t, ctx, conn, req2)
	if notif == nil {
		t.Fatal("the finalize page must send for an overdue head exactly as a one-page evaluation does")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["balanceCents"].(float64); got != 1900 {
		t.Fatalf("params.balanceCents = %v, want 1900 (25 × 100 − 6 × 100) — the aggregate's balance is the account's", got)
	}

	// A re-dispatch over the finished history: 31 entries is still two pages
	// from a fresh start (the finalize page carries no checkpoint forward), so
	// it checkpoints again at phase a and sends nothing — the episode is
	// already reminded for.
	_, req3 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrtwoeval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-24T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, req3) != nil {
		t.Fatal("the episode is already reminded for")
	}
	if got, _ := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))["phase"].(string); got != loftspaceledger.ArrearsPhaseA {
		t.Fatalf("a fresh evaluation over a two-page history starts again at phase %q, got %q", loftspaceledger.ArrearsPhaseA, got)
	}
}

// TestArrears_PostedEntryMidReplayRestartsIt pins the checkpoint's reset
// boundary. A posted entry changes the set the cursor pages over, so a
// checkpoint taken before it no longer describes a prefix of the history:
// post_entry (which has no balance to distinguish shapes with, so it drops the
// checkpoint on every entry alike) marks the state stale, the phase gap
// closes, missing_evaluation re-opens, and the next evaluation starts again at
// page 1 — never resumes a cursor over a set that has moved under it.
func TestArrears_PostedEntryMidReplayRestartsIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrestart")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSRSTLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSRSTACCTHJKM", leaseKey)
	for i := 0; i <= loftspaceledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarrrsteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsReplay(t, arrearsData(t, ctx, conn, acctKey)) == nil {
		t.Fatal("fixture: the first dispatch must leave a checkpoint")
	}

	// A posted entry mid-replay.
	debitAt(t, ctx, conn, cp, cons, "bbarrrstdebit0000001", acctKey, "2026-08-25T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, after) != nil {
		t.Fatalf("a posted entry must drop the checkpoint — the set under its cursor has changed: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and mark the state stale, which is what re-opens the evaluation gap: %+v", after)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarrrsteval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-26T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("32 entries are two pages, so the restarted evaluation checkpoints again")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — the evaluation restarts at page 1, it does not resume a cursor over a moved set", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != loftspaceledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, loftspaceledger.ArrearsPhaseA)
	}
}

// TestArrears_MalformedCheckpointRestartsAtPageOne pins the op's side of the
// hardening the lens pin TestLoftspaceArrears_MalformedCheckpointReopensEvaluation
// covers: a recorded checkpoint the op cannot resume — an unknown phase here —
// is treated as absent, so the evaluation the re-opened gap dispatches starts
// at page 1 over a fresh aggregate and records a well-formed checkpoint in its
// place, rather than folding onto a corrupt one or refusing.
func TestArrears_MalformedCheckpointRestartsAtPageOne(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbadcheckpoint")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSBADLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSBADACCTHJKM", leaseKey)
	for i := 0; i <= loftspaceledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
		"replay": map[string]any{
			"phase":  "x",
			"cursor": "lnk.transaction." + budgetTxID(5) + ".postedTo.account.BBARREARSBADACCTHJKM",
			"pages":  7,
			"entries": map[string]any{
				budgetTxID(0): map[string]any{"postedAt": "2026-01-01T00:00:00Z", "type": "debit", "amountCents": 999999},
			},
		},
	})

	evaluateArrears(t, ctx, conn, cp, cons, "bbarrbadeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("31 entries are two pages, so the restarted evaluation checkpoints")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — an unresumable checkpoint is treated as absent, not continued from", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != loftspaceledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, loftspaceledger.ArrearsPhaseA)
	}
	entries, _ := replay["entries"].(map[string]any)
	if len(entries) != loftspaceledger.ArrearsPageLimit {
		t.Fatalf("replay.entries carries %d rows, want %d — a fresh aggregate over page 1, not the corrupt one folded onto", len(entries), loftspaceledger.ArrearsPageLimit)
	}
	if e, _ := entries[budgetTxID(0)].(map[string]any); e == nil || e["amountCents"].(float64) != 100 {
		t.Fatalf("the corrupt aggregate's entry must be replaced by the page's own reading: %v", entries[budgetTxID(0)])
	}
}

// TestArrears_RedeliveredPageAdvancesNeverRepeats pins the replay-under-
// redelivery rule: a dispatch that finds a checkpoint consumes the NEXT page,
// so two dispatches after page 1 advance the checkpoint 1 → 2 (never 1 → 1),
// and the phase alternates a → b — the flip that closes the gap that
// dispatched the page and opens its sibling. Three pages of history so the
// second dispatch is still mid-replay; the third finalizes.
func TestArrears_RedeliveredPageAdvancesNeverRepeats(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsadvance")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSADVLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSADVACCTHJKM", leaseKey)
	for i := 0; i < 2*loftspaceledger.ArrearsPageLimit+1; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarradveval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if first == nil {
		t.Fatal("fixture: page 1 must checkpoint")
	}
	if pages, _ := first["pages"].(float64); pages != 1 {
		t.Fatalf("after one dispatch replay.pages = %v, want 1", first["pages"])
	}
	if phase, _ := first["phase"].(string); phase != loftspaceledger.ArrearsPhaseA {
		t.Fatalf("after one dispatch replay.phase = %q, want %q", phase, loftspaceledger.ArrearsPhaseA)
	}
	firstCursor, _ := first["cursor"].(string)

	evaluateArrears(t, ctx, conn, cp, cons, "bbarradveval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	second := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if second == nil {
		t.Fatal("61 entries are three pages, so the second dispatch is still mid-replay")
	}
	if pages, _ := second["pages"].(float64); pages != 2 {
		t.Fatalf("after two dispatches replay.pages = %v, want 2 — a redelivered dispatch advances, it never repeats the page", second["pages"])
	}
	if phase, _ := second["phase"].(string); phase != loftspaceledger.ArrearsPhaseB {
		t.Fatalf("after two dispatches replay.phase = %q, want %q — the flip is what closes missing_replay_a and opens missing_replay_b", phase, loftspaceledger.ArrearsPhaseB)
	}
	if cursor, _ := second["cursor"].(string); cursor == "" || cursor <= firstCursor {
		t.Fatalf("the cursor must advance past page 1's (%q), got %q", firstCursor, cursor)
	}
	if entries, _ := second["entries"].(map[string]any); len(entries) != 2*loftspaceledger.ArrearsPageLimit {
		t.Fatalf("replay.entries carries %d rows after two pages, want %d", len(entries), 2*loftspaceledger.ArrearsPageLimit)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "bbarradveval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the third page exhausts the enumeration and finalizes: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != "2026-05-01T12:00:00Z" {
		t.Fatalf("dueAt = %q, want %q over all 61 charges", got, "2026-05-01T12:00:00Z")
	}
}

// TestArrears_MidReplayCarriesTheEpisodeRecord pins what a page WRITES: the
// checkpoint, and every other recorded field verbatim. A page has evaluated
// nothing, so the episode's due date, the head it was reminded for, the send
// record and the stale mark all still describe the account exactly as the last
// completed evaluation or posted entry left them — dropping the send record
// here would send twice for one debt once the finalize page found the head
// overdue — and evaluatedAt keeps naming that last completed evaluation.
// Nothing is sent.
func TestArrears_MidReplayCarriesTheEpisodeRecord(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscarry")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSCRYLEASEHJK")
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBARREARSCRYACCTHJKM", leaseKey)
	for i := 0; i <= loftspaceledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seeded := map[string]any{
		"dueAt":       "2026-05-16T12:00:00Z",
		"remindAt":    "2026-05-21T12:00:00Z",
		"remindedFor": "2026-05-16T12:00:00Z",
		"sentAt":      "2026-05-17T09:00:00Z",
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", seeded)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrcryeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a page sends nothing: %+v", notif)
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, data) == nil {
		t.Fatalf("a checkpoint is the one thing the page adds: %+v", data)
	}
	for field, want := range seeded {
		if got := data[field]; got != want {
			t.Fatalf("%s = %v, want %v carried verbatim — a page evaluates nothing and may rewrite nothing", field, got, want)
		}
	}
	if len(data) != len(seeded)+1 {
		t.Fatalf("the page writes the seeded fields plus replay and nothing else, got %+v", data)
	}
}

// TestArrears_WrongClassRefused pins the class check at every writer: a
// .arrears document of any other class is a fault to refuse, never state to
// carry (post_entry, for each of the three entry ops) or to decide a send on
// (the evaluation).
func TestArrears_WrongClassRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearswrongclass")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSWRCLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrwrcacct00000001", leaseKey)
	seedAspect(t, ctx, conn, acctKey, "arrears", "somethingElse", map[string]any{"evaluatedAt": "2026-09-01T09:00:00Z"})

	for i, opType := range []string{"DebitAccount", "LoftspaceRecordCharge", "CreditAccount"} {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("bbarrwrcentry000000" + strconv.Itoa(i)),
			Lane:          processor.LaneDefault,
			OperationType: opType,
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-09-01T09:00:00Z",
			Class:         "transaction",
			Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"memo":"Rent"}`),
			ContextHint:   &processor.ContextHint{Reads: []string{acctKey}, OptionalReads: []string{acctKey + ".arrears"}},
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
			t.Fatalf("%s over a wrong-class .arrears: outcome = %v error = %+v, want an InvalidState rejection", opType, outcome, reply.Error)
		}
	}

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "bbarrwrceval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("an evaluation over a wrong-class .arrears: want an InvalidState rejection, got %+v", reply.Error)
	}
}

// TestArrears_UndeclaredSubmitterStillHydratesArrears is the derive_reads
// proof for all four governed ops: a submitter that declares NOTHING — no
// Reads, no OptionalReads — still has the account root and its .arrears
// aspect hydrated by the DDLs' own derive_reads, so the stale mark lands on
// a conditioned key and the evaluation sees the record it must carry. The
// read-drift guard armed on the pipeline is what makes this a proof rather
// than a shape check: a lazy fallback read would fail the test at the read.
// Both walks stay declared on the evaluation — only a read can be derived
// server-side.
func TestArrears_UndeclaredSubmitterStillHydratesArrears(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsundeclared")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSUNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrundacct00000001", leaseKey)
	// The charge the seeded send was for: it must predate sentAt, or the
	// evaluation rightly reads the record as a finished episode's and drops it.
	seedEntryAt(t, ctx, conn, acctKey, "BBARREARSUNDTXNAHJKM", "debit", 240000, "2026-08-01T09:00:00Z", "")
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"dueAt":       "2026-08-01T09:00:00Z",
		"remindAt":    "2026-08-06T09:00:00Z",
		"remindedFor": "2026-08-01T09:00:00Z",
		"sentAt":      "2026-08-07T09:00:00Z",
		"evaluatedAt": "2026-08-07T09:00:00Z",
	})

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("bbarrunddebit0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-20T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":4500,"memo":"Late fee"}`),
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	afterDebit := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := afterDebit["stale"].(bool); !stale {
		t.Fatalf("the stale mark must land even when the submitter declared nothing about .arrears: %+v", afterDebit)
	}
	if got, _ := afterDebit["sentAt"].(string); got != "2026-08-07T09:00:00Z" {
		t.Fatalf("sentAt = %q — the derived hydration is what lets the entry carry the send record", got)
	}

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("bbarrundcharge000001"),
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceRecordCharge",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-20T10:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Lock change"}`),
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	afterCharge := arrearsData(t, ctx, conn, acctKey)
	if got, _ := afterCharge["remindedFor"].(string); got != "2026-08-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q — an undeclared landlord charge must carry the record forward too", got)
	}

	creditEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("bbarrundcredit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-21T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500}`),
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	afterCredit := arrearsData(t, ctx, conn, acctKey)
	if got, _ := afterCredit["remindAt"].(string); got != "2026-08-06T09:00:00Z" {
		t.Fatalf("remindAt = %q — an undeclared credit must carry the record forward too", got)
	}

	// And the Weaver-dispatched evaluation, whose own derive_reads declares the
	// key for a dispatcher that omitted it: it must see the carried sentAt and
	// send NOTHING for this already-reminded episode.
	evalReqID := testutil.GenReqID("bbarrundeval00000001")
	evalEnv := &processor.OperationEnvelope{
		RequestID:     evalReqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateLoftspaceArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-22T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
				{Hub: acctKey, Relation: "heldFor", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, evalEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, evalReqID); notif != nil {
		t.Fatalf("an undeclared evaluation must still see the carried send record and send nothing: %+v", notif)
	}
	evaluated := arrearsData(t, ctx, conn, acctKey)
	if got, _ := evaluated["sentAt"].(string); got != "2026-08-07T09:00:00Z" {
		t.Fatalf("sentAt = %q — the evaluation must see and carry the hydrated record", got)
	}
	if _, ok := evaluated["stale"]; ok {
		t.Fatalf("the evaluation must clear the stale mark it was dispatched for: %+v", evaluated)
	}
}

// recordArrearsNotification submits one RecordLoftspaceArrearsReminderNotification
// exactly as the bridge does: no Class (the Processor's operationType→class
// reverse index resolves the handler) and NO ContextHint at all — the generic
// dispatch path declares nothing, which is why the op reads nothing and why
// externalRef is the only thing that decides which vertex it writes to.
func recordArrearsNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, externalRef, status, submittedAt string, want processor.MessageOutcome) *processor.OperationReply {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordLoftspaceArrearsReminderNotification",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Payload:       json.RawMessage(`{"externalRef":"` + externalRef + `","status":"` + status + `","result":"notification sent"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply
}

// arrearsNotificationOutcome reads the audit aspect the replyOp writes.
func arrearsNotificationOutcome(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, acctKey+".arrearsNotification")
	if cls, _ := doc["class"].(string); cls != "loftspaceAccountArrearsNotification" {
		t.Fatalf("%s.arrearsNotification class = %q, want loftspaceAccountArrearsNotification", acctKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.arrearsNotification carries no data", acctKey)
	}
	return data
}

// TestArrearsNotification_ForgedExternalRefRefused (l). externalRef arrives
// from OUTSIDE the platform — the adapter echoes it back through the bridge —
// and it is the op's only say over which vertex the outcome aspect is hung
// on. Splitting it and trusting the left half means any 3-segment vtx key
// names a target: an externalRef of "vtx.identity.<NanoID>:<dueAt>" would
// write a loftspaceAccountArrearsNotification onto a TENANT'S IDENTITY, a
// vertex this package has no business touching at all. The type check is
// what stops it.
//
// The accepted vector runs first, so the refusal below is attributable to
// the type and not to a guard that denies every reply — the two submissions
// differ in exactly one segment. A SECOND accepted reply for a later episode
// then proves the overwrite: create-only would have refused it.
func TestArrearsNotification_ForgedExternalRefRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnotif")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSNTFLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrntfacct00000001", leaseKey)

	const headA = "vtx.transaction.BBARREARSNTFTXNAHJKM"
	recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrntfok0000000001",
		acctKey+":2026-09-01T09:00:00Z:"+headA, "completed", "2026-09-12T09:00:05Z", processor.OutcomeAccepted)
	outcome := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := outcome["status"].(string); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got, _ := outcome["remindedFor"].(string); got != "2026-09-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the dueAt half of the externalRef", got)
	}
	if got, _ := outcome["sentAt"].(string); got != "2026-09-12T09:00:05Z" {
		t.Fatalf("sentAt = %q, want the reply's own submittedAt", got)
	}

	// The same submission with the account key's TYPE segment swapped.
	victim := "vtx.identity." + ledgerActorID
	reply := recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrntfforged000001",
		victim+":2026-09-01T09:00:00Z:"+headA, "completed", "2026-09-12T09:00:05Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want an InvalidArgument rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "externalRef") {
		t.Fatalf("the refusal must name the field it refused, got %q", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, victim+".arrearsNotification") {
		t.Fatalf("a forged externalRef wrote an aspect onto %s — the op must touch nothing but an account", victim)
	}

	// A token of any other shape is refused whole rather than partially
	// trusted: the two-part legacy shape, a dueAt that is not the canonical
	// whole-second UTC instant, and a third segment that is not a
	// transaction key. Nothing is written for any of them.
	for i, bad := range []string{
		acctKey + ":2026-09-01T09:00:00Z",
		acctKey + ":2026-09-01T09:00:00.5Z:" + headA,
		acctKey + ":2026-09-01 09:00:00Z:" + headA,
		acctKey + ":2026-09-01T09:00:00Z:vtx.identity." + ledgerActorID,
		acctKey + ":2026-09-01T09:00:00Z:",
		acctKey + ":2026-09-01T09:00:00Z-" + headA,
	} {
		reply := recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrntfbadshape000"+strconv.Itoa(i),
			bad, "completed", "2026-09-12T09:00:05Z", processor.OutcomeRejected)
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") || !strings.Contains(reply.Error.Message, "externalRef") {
			t.Fatalf("token %q: want an InvalidArgument rejection naming externalRef, got %+v", bad, reply.Error)
		}
	}
	if got, _ := arrearsNotificationOutcome(t, ctx, conn, acctKey)["sentAt"].(string); got != "2026-09-12T09:00:05Z" {
		t.Fatalf("a refused token must leave the recorded outcome untouched, sentAt = %q", got)
	}

	// A later episode replies onto the SAME key and replaces the record —
	// arrears recur, and a create-only write would have refused this.
	recordArrearsNotification(t, ctx, conn, cp, cons, "bbarrntfok0000000002",
		acctKey+":2026-10-01T09:00:00Z:vtx.transaction.BBARREARSNTFTXNBHJKM", "failed", "2026-10-12T09:00:05Z", processor.OutcomeAccepted)
	second := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := second["remindedFor"].(string); got != "2026-10-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the later episode's dueAt — the aspect holds the LATEST episode's outcome", got)
	}
	if got, _ := second["status"].(string); got != "failed" {
		t.Fatalf("status = %q, want the later episode's verdict", got)
	}
}
