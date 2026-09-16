package clinicledger_test

// The arrears reminder, end to end through the real Processor pipeline: the
// .arrears episode aspect post_entry opens, ends and marks stale off the
// account's maintained .balance, the head EvaluateClinicArrears recomputes,
// the one-notification-per-EPISODE guarantee, the actor guard, the patient
// resolution, the replay-budget degrade, and the bridge's replyOp. The
// vectors are cafe-ledger's TestArrears_* set, every .balance branch included
// — this ledger stores a balance exactly as café's does, so a posted entry
// can itself open or end an episode, and the evaluation recomputes only the
// head a partial payment moved.

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
	clinicledger "github.com/operatinggraph/lattice/packages/clinic-ledger"
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
			{OperationType: "EvaluateClinicArrears", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// arrearsHint is the contextHint the clinicArrearsReminders playbook
// dispatches with (targets.go): the account root, its absence-tolerant
// .arrears aspect, and the bounded postedTo replay + the heldFor patient walk.
// The per-transaction .entry reads and the per-credit reverses hops that
// replay discovers are NOT declared — their keys are data-derived, the
// class-(e) split.
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

// evaluateArrears drives one EvaluateClinicArrears as `actor` at
// `submittedAt`, asserts the outcome, and returns the reply (for a refusal's
// message) and the request id (for the outbox the notification rides on).
// Class is LEFT EMPTY, exactly as Weaver's actuator dispatches a directOp — it
// relies on the Processor's operationType→class reverse index, which resolves
// to the clinicaccount vertexType handler.
func evaluateArrears(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actor, acctKey, submittedAt string,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateClinicArrears",
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
// aspect is absent — which is a real state (a never-charged account carries
// none), not an error.
func arrearsData(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	if !keyExists(t, ctx, conn, acctKey+".arrears") {
		return nil
	}
	doc := readDoc(t, ctx, conn, acctKey+".arrears")
	if cls, _ := doc["class"].(string); cls != "clinicAccountArrears" {
		t.Fatalf("%s.arrears class = %q, want clinicAccountArrears", acctKey, cls)
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

// debitAt posts a charge at an explicit instant — the arrears vectors turn on
// WHEN a charge posted. Declares the account and its .balance, exactly as
// the existing dispatchers do; the .arrears key is the DDL's own derive_reads'
// to supply.
func debitAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Office visit copay"}`),
		ContextHint:   staffDebitHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.clinictransaction." + nanoIDFromRequestID(reqID)
}

// creditAt is debitAt's payment counterpart: a staff-voice payment, so it is
// not capped by the balance and may take the account into credit. A non-empty
// reversesKey posts the credit as a waiver that names the charge it gives
// back (the reverses link), the shape clinicNoShowSettlement's
// missing_reversal playbook dispatches.
func creditAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int, reversesKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload := `{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Front-desk payment"}`
	reads := []string{acctKey}
	if reversesKey != "" {
		payload = `{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"reason":"waiver","reversesRef":"` + reversesKey + `"}`
		reads = append(reads, reversesKey)
	}
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "clinictransaction",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: []string{acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.clinictransaction." + nanoIDFromRequestID(reqID)
}

// seedEntryAt seeds one already-posted transaction on acctKey at an EXPLICIT
// postedAt — the vertex, its .entry aspect and the postedTo link back — the
// shape a committed entry sits in, with no op run. A non-empty reversesTxID
// also seeds the reverses link a waiver naming that charge writes.
func seedEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int, postedAt, reversesTxID string) {
	t.Helper()
	txKey := "vtx.clinictransaction." + txID
	acctID := acctKey[len("vtx.clinicaccount."):]
	seedVertex(t, ctx, conn, txKey, "clinictransaction", map[string]any{})
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": entryType, "amountCents": amountCents, "postedAt": postedAt,
	})
	seedLink(t, ctx, conn,
		"lnk.clinictransaction."+txID+".postedTo.clinicaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
	if reversesTxID != "" {
		seedLink(t, ctx, conn,
			"lnk.clinictransaction."+txID+".reverses.clinictransaction."+reversesTxID,
			txKey, "vtx.clinictransaction."+reversesTxID, "reverses", "reverses")
	}
}

// dueFor is a charge's postedAt plus the package's net term — the same
// arithmetic the patient's own statement runs, from the package's constant
// rather than a second literal.
func dueFor(t *testing.T, postedAt string) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339, postedAt)
	if err != nil {
		t.Fatalf("parse %q: %v", postedAt, err)
	}
	return at.AddDate(0, 0, clinicledger.ArrearsGraceDays).Format(time.RFC3339)
}

// openAccount registers a patient and opens their ledger account, returning
// both keys.
func openAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, fullName string) (patientKey, acctKey string) {
	t.Helper()
	patientKey = createPatient(t, ctx, conn, cp, cons, label+"pat", fullName)
	acctKey = createAccount(t, ctx, conn, cp, cons, label+"acct", patientKey)
	return patientKey, acctKey
}

// TestArrears_FirstChargeOpensAnEpisode (a). A charge posted to an account that
// owes nothing IS the FIFO head, so post_entry can name the due date without
// replaying anything: this charge's own postedAt plus the package's net term.
func TestArrears_FirstChargeOpensAnEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsopen")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarropen", "Riley Chen")
	if arrearsData(t, ctx, conn, acctKey) != nil {
		t.Fatal("ClinicCreateAccount must mint NO .arrears — a new account owes nothing, and its missing evaluatedAt is what opens the gap once")
	}

	debitAt(t, ctx, conn, cp, cons, "clarropendebit000001", acctKey, "2026-08-01T09:00:00Z", 2500)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("a charge against an account that owed nothing must open an arrears episode")
	}
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-01T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q (the charge's own postedAt + the net term)", got, dueFor(t, "2026-08-01T09:00:00Z"))
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-01T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the charge's postedAt", got)
	}
	if _, ok := data["stale"]; ok {
		t.Fatal("a freshly opened episode is not stale")
	}
}

// TestArrears_SecondChargeLeavesTheHead (b). The head is the OLDEST open
// charge, and a second charge queues behind it — re-stamping dueAt here would
// push a weeks-old debt's due date back to the day of the newest visit.
func TestArrears_SecondChargeLeavesTheHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssecond")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrsnd", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrsnddebit0000001", acctKey, "2026-08-01T09:00:00Z", 2500)
	first := arrearsData(t, ctx, conn, acctKey)

	debitAt(t, ctx, conn, cp, cons, "clarrsnddebit0000002", acctKey, "2026-08-20T09:00:00Z", 500)

	second := arrearsData(t, ctx, conn, acctKey)
	if second["dueAt"] != first["dueAt"] {
		t.Fatalf("dueAt moved from %v to %v on a second charge — the head is the OLDEST open charge", first["dueAt"], second["dueAt"])
	}
	if second["evaluatedAt"] != first["evaluatedAt"] {
		t.Fatalf("a charge that changes nothing must write nothing; evaluatedAt moved from %v to %v", first["evaluatedAt"], second["evaluatedAt"])
	}
}

// TestArrears_PartialPaymentMarksStale (c). A partial payment can move the FIFO
// head to a later charge with a later due date, which no single entry can
// compute — so post_entry marks the recorded state stale rather than guessing,
// and the convergence lens reads stale as an open gap.
func TestArrears_PartialPaymentMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsstale")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrstl", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrstldebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "clarrstldebit0000002", acctKey, "2026-08-20T09:00:00Z", 500)
	before := arrearsData(t, ctx, conn, acctKey)

	creditAt(t, ctx, conn, cp, cons, "clarrstlpay000000001", acctKey, "2026-08-21T09:00:00Z", 1000, "")

	after := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("a partial payment must mark the recorded arrears state stale, got %+v", after)
	}
	if after["dueAt"] != before["dueAt"] {
		t.Fatalf("the stale mark is an ADDITION, not a rewrite: dueAt moved from %v to %v", before["dueAt"], after["dueAt"])
	}
}

// TestArrears_PaymentToZeroEndsTheEpisode (d). Paying the balance off rewrites
// .arrears to {evaluatedAt} alone: no dueAt, so no timer stays armed, and
// nothing of the finished episode survives to make the NEXT charge look already
// reminded.
func TestArrears_PaymentToZeroEndsTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscleared")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrclr", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrclrdebit0000001", acctKey, "2026-08-01T09:00:00Z", 2500)
	creditAt(t, ctx, conn, cp, cons, "clarrclrpay000000001", acctKey, "2026-08-05T09:00:00Z", 2500, "")

	data := arrearsData(t, ctx, conn, acctKey)
	if _, ok := data["dueAt"]; ok {
		t.Fatalf("a paid-off account must carry no dueAt, got %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("a paid-off account is not stale — there is nothing to recompute: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-05T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the payment's postedAt", got)
	}
}

// TestArrears_DebitOnACreditBalanceOpensNoEpisode (d2). "The account owed
// nothing before this charge" is B ≤ 0, but that is not the same question as
// "does it owe anything after it". A staff credit is not capped by the balance
// — a waiver larger than what is owed takes the account into CREDIT — and a
// charge that only eats into that credit leaves the patient still owed money
// by the clinic: under the FIFO the surplus prepays the charge outright, so
// there is no open debit and no head. An episode minted there arms a timer
// that reminds a patient about money they do not owe. The SECOND charge, the
// one that finally takes the balance positive, is the one that opens the
// episode — and its own postedAt is the term the patient is held to.
func TestArrears_DebitOnACreditBalanceOpensNoEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscredit")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrcrd", "Riley Chen")

	// Charge 1000, then a staff credit of 2000: B = -1000.
	debitAt(t, ctx, conn, cp, cons, "clarrcrddebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	creditAt(t, ctx, conn, cp, cons, "clarrcrdpay000000001", acctKey, "2026-08-05T09:00:00Z", 2000, "")
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("fixture precondition: balanceCents = %v, want -1000 (the account is in credit)", got)
	}

	// B = -1000 ≤ 0 AND B′ = -500 ≤ 0: still in credit, so no episode.
	debitAt(t, ctx, conn, cp, cons, "clarrcrddebit0000002", acctKey, "2026-08-20T09:00:00Z", 500)
	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("fixture precondition: the credit that cleared the balance must have written {evaluatedAt}")
	}
	if _, ok := data["dueAt"]; ok {
		t.Fatalf("a charge that leaves the account IN CREDIT must open no episode — the patient owes nothing: %+v", data)
	}

	// B = -500 ≤ 0 AND B′ = +100 > 0: NOW the episode opens, on this charge.
	debitAt(t, ctx, conn, cp, cons, "clarrcrddebit0000003", acctKey, "2026-08-21T09:00:00Z", 600)
	data = arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-21T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the charge that actually took the account into arrears starts the term", got, dueFor(t, "2026-08-21T09:00:00Z"))
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-21T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want that charge's postedAt", got)
	}
}

// TestArrears_EvaluateSendsOnceThenNothing (e) is the green bar: a patient past
// the net term is reminded ONCE per episode. The first evaluation stamps
// remindedFor + sentAt and emits the external.notification the bridge turns into
// a real message; a re-dispatch recomputes the same head, finds sentAt already
// recorded, and emits nothing at all.
func TestArrears_EvaluateSendsOnceThenNothing(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssend")

	patientKey, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrsend", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrsenddebit000001", acctKey, "2026-08-01T09:00:00Z", 2500)
	wantDue := dueFor(t, "2026-08-01T09:00:00Z")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "clarrsendeval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q (the FIFO head recomputed from the account's own history)", got, wantDue)
	}
	if got, _ := data["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q — this is what closes the gap for the episode", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("an overdue head must emit external.notification — the send is the whole point of the target")
	}
	wantRef := acctKey + ":" + wantDue
	if got, _ := notif["externalRef"].(string); got != wantRef {
		t.Fatalf("externalRef = %q, want %q (the episode key the adapter dedups on)", got, wantRef)
	}
	if got, _ := notif["idempotencyKey"].(string); got != wantRef {
		t.Fatalf("idempotencyKey = %q, want %q", got, wantRef)
	}
	if got, _ := notif["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := notif["replyOp"].(string); got != "RecordClinicArrearsReminderNotification" {
		t.Fatalf("replyOp = %q, want RecordClinicArrearsReminderNotification", got)
	}
	params, _ := notif["params"].(map[string]any)
	if params == nil {
		t.Fatalf("the notification carries no params: %+v", notif)
	}
	if got, _ := params["accountKey"].(string); got != acctKey {
		t.Fatalf("params.accountKey = %q, want %q", got, acctKey)
	}
	if got, _ := params["patientKey"].(string); got != patientKey {
		t.Fatalf("params.patientKey = %q, want %q (resolved live off the account's own heldFor link, not the payload)", got, patientKey)
	}
	if got, _ := params["reminderType"].(string); got != "clinicArrears" {
		t.Fatalf("params.reminderType = %q, want clinicArrears", got)
	}
	if got, _ := params["dueAt"].(string); got != wantDue {
		t.Fatalf("params.dueAt = %q, want %q", got, wantDue)
	}
	if got, _ := params["balanceCents"].(float64); got != 2500 {
		t.Fatalf("params.balanceCents = %v, want 2500", got)
	}

	// The re-dispatch. Same account, same history, a later instant: the head is
	// unchanged, sentAt already records the send, and NOTHING goes out.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "clarrsendeval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("a re-dispatched evaluation must send NOTHING for an episode already reminded for, got %+v", notif)
	}
	again := arrearsData(t, ctx, conn, acctKey)
	if got, _ := again["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt was re-stamped to %q — the original send record must be carried forward, not rewritten", got)
	}
	if got, _ := again["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q carried forward", got, wantDue)
	}
}

// TestArrears_EvaluateRearmsOnACoveredHead (f). A payment covered the charge the
// recorded due date came from, so the FIFO head is now a LATER charge with a
// later due date. The evaluation recomputes it, clears stale, carries no send
// record (none was made), and the recomputed date re-arms the timer — with no
// notification, because nothing is overdue yet.
func TestArrears_EvaluateRearmsOnACoveredHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrearm")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrrearm", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrrearmdebit00001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "clarrrearmdebit00002", acctKey, "2026-08-20T09:00:00Z", 500)
	creditAt(t, ctx, conn, cp, cons, "clarrrearmpay0000001", acctKey, "2026-08-21T09:00:00Z", 1000, "")
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the partial payment must have marked the state stale")
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "clarrrearmeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	// The Aug 1 charge was fully paid off, so the head is the Aug 20 one.
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-20T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the FIFO head moved to the surviving charge", got, dueFor(t, "2026-08-20T09:00:00Z"))
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for, so it must clear it: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("nothing has been reminded for this episode: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a head that is not yet due must send nothing, got %+v", notif)
	}
}

// TestArrears_EvaluateNetsAReversalAgainstItsCharge proves arrears_head's
// netting pre-pass matches the statement's own rule (cmd/clinic-app/ledger.go
// deriveStatement), not plain FIFO: a waiver that names the charge it reverses
// (reversesRef) retires that charge directly. Two charges A (Aug 1) and B
// (Aug 20), then a reversal of B: under plain FIFO the credit would retire A,
// the OLDER charge, and leave B as the head with a Sep 4 due date; the netting
// rule reads the reverses link, retires B specifically, and leaves A — the
// charge the patient actually still owes — as the head.
func TestArrears_EvaluateNetsAReversalAgainstItsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnetrev")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrnet", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrnetdebita000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	chargeB := debitAt(t, ctx, conn, cp, cons, "clarrnetdebitb000001", acctKey, "2026-08-20T09:00:00Z", 1000)
	creditAt(t, ctx, conn, cp, cons, "clarrnetrev000000001", acctKey, "2026-08-21T09:00:00Z", 1000, chargeB)

	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the reversal left a balance, so it must have marked the state stale")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "clarrneteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-01T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the reversal retires B directly, leaving A (Aug 1) as the head", got, dueFor(t, "2026-08-01T09:00:00Z"))
	}
}

// TestArrears_OneNotificationPerEpisodeNotPerHead (f2) is the once-per-episode
// guarantee at its only hard case, and the vector the design mandates. An
// EPISODE is the stretch from the charge that takes the account from square to
// owing until the balance comes back to zero; the FIFO HEAD moves within one
// episode every time a partial payment retires the oldest charge. A patient
// who pays SOMETHING off is doing the right thing, and if the send were keyed
// on the head — on remindedFor naming this dueAt — every part-payment would
// hand them a second nag for the same continuous debt, because the charge the
// head moves to is usually past its own term too. The send is keyed on
// sentAt's ABSENCE instead, which is a fact about the episode. remindedFor is
// still written every time, because that is what closes the convergence gap;
// the two are deliberately different questions.
func TestArrears_OneNotificationPerEpisodeNotPerHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsepisode")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarreps", "Riley Chen")

	debitAt(t, ctx, conn, cp, cons, "clarrepsdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "clarrepsdebit0000002", acctKey, "2026-08-10T09:00:00Z", 1000)
	dueC1 := dueFor(t, "2026-08-01T09:00:00Z")
	dueC2 := dueFor(t, "2026-08-10T09:00:00Z")

	// The first charge falls due and the reminder goes out.
	_, reqID1 := evaluateArrears(t, ctx, conn, cp, cons, "clarrepseval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-17T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID1) == nil {
		t.Fatal("the first overdue head in an episode must send")
	}
	sent := arrearsData(t, ctx, conn, acctKey)
	if got, _ := sent["remindedFor"].(string); got != dueC1 {
		t.Fatalf("remindedFor = %q, want %q", got, dueC1)
	}
	if got, _ := sent["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	// A payment of EXACTLY the first charge's amount retires it. The balance
	// stays positive, so the episode continues; the head moves to the second
	// charge, whose own term has also passed — and nothing may go out for it.
	creditAt(t, ctx, conn, cp, cons, "clarrepspay000000001", acctKey, "2026-08-18T09:00:00Z", 1000, "")
	moved := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := moved["stale"].(bool); !stale {
		t.Fatalf("a partial payment marks the state stale: %+v", moved)
	}
	if got, _ := moved["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the stale mark carries the send record, never drops it", got)
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "clarrepseval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-27T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("the head moved WITHIN one episode — a patient paying their balance down must not be nagged twice: %+v", notif)
	}
	moved = arrearsData(t, ctx, conn, acctKey)
	if got, _ := moved["dueAt"].(string); got != dueC2 {
		t.Fatalf("dueAt = %q, want %q (the head moved to the surviving charge)", got, dueC2)
	}
	if got, _ := moved["remindedFor"].(string); got != dueC2 {
		t.Fatalf("remindedFor = %q, want %q — remindedFor tracks the HEAD, and leaving it on the retired charge holds the convergence gap open forever", got, dueC2)
	}
	if got, _ := moved["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the episode's send record is carried, not re-stamped", got)
	}
	if _, ok := moved["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for: %+v", moved)
	}

	// Paying the balance off ENDS the episode: the send record goes with it.
	creditAt(t, ctx, conn, cp, cons, "clarrepspay000000002", acctKey, "2026-08-28T09:00:00Z", 1000, "")
	cleared := arrearsData(t, ctx, conn, acctKey)
	if _, ok := cleared["sentAt"]; ok {
		t.Fatalf("a paid-off account keeps no send record — the NEXT episode must be able to send: %+v", cleared)
	}
	if _, ok := cleared["dueAt"]; ok {
		t.Fatalf("a paid-off account carries no dueAt: %+v", cleared)
	}

	// A NEW charge, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "clarrepsdebit0000003", acctKey, "2026-08-29T09:00:00Z", 500)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; ok {
		t.Fatal("a fresh episode starts with no send record")
	}
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "clarrepseval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID3) == nil {
		t.Fatal("a NEW episode past its term must send — one per episode is not one per account")
	}
}

// TestArrears_ForgedSendRefused (g) is the actor guard. ledgerActorKey holds
// the operator role AND the identical Scope:"any" EvaluateClinicArrears grant,
// so step 3 authorizes it; only `op.actor != primordialActor["weaver"]` stops
// it from having the platform tell an arbitrary patient they owe money.
// Nothing is written either — a marker minted on a forged send would close
// the gap and suppress the real reminder. A payload carrying sentAt /
// remindedFor is the same forgery through a different door: the op reads
// neither field, so nothing but the account's own hydrated state decides what
// is stamped.
func TestArrears_ForgedSendRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsforged")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrfgd", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrfgddebit0000001", acctKey, "2026-08-01T09:00:00Z", 2500)
	before := arrearsData(t, ctx, conn, acctKey)

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "clarrfgdeval00000001",
		ledgerActorKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if _, ok := after["sentAt"]; ok {
		t.Fatalf("a refused evaluation must record no send: %+v", after)
	}
	if after["evaluatedAt"] != before["evaluatedAt"] {
		t.Fatalf("a refused evaluation must write nothing at all; evaluatedAt moved from %v to %v", before["evaluatedAt"], after["evaluatedAt"])
	}

	// The payload door: a Weaver-actor submission whose payload carries a
	// send record. The op takes accountKey alone; sentAt and remindedFor come
	// from the account's own hydrated state, so the extra fields change
	// nothing about what is stamped — the head is not yet due at this
	// instant, so neither field may appear.
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clarrfgdeval00000002"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateClinicArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-05T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","sentAt":"2026-08-02T09:00:00Z","remindedFor":"2026-08-02T09:00:00Z"}`),
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
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-05T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the accepted evaluation's own submittedAt", got)
	}
}

// TestArrears_PatientResolvedFromAccountState (g2a). patientKey never travels
// through the payload — a Weaver Params reference to a null lens column (an
// account with no live heldFor patient) would otherwise make the strategist
// refuse to dispatch the row at all (internal/weaver/strategist.go), silently
// starving the very accounts most worth aging. The op resolves the patient
// itself, live, off the account's own heldFor out-link, so it decides this
// with no payload field to trust or forge; a payload patientKey naming a
// stranger is ignored.
func TestArrears_PatientResolvedFromAccountState(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearspatientstate")

	patientKey, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrpts", "Riley Chen")
	stranger := createPatient(t, ctx, conn, cp, cons, "clarrptsstranger0001", "Someone Else")
	debitAt(t, ctx, conn, cp, cons, "clarrptsdebit0000001", acctKey, "2026-08-01T09:00:00Z", 2500)

	reqID := testutil.GenReqID("clarrptseval00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateClinicArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-22T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","patientKey":"` + stranger + `"}`),
		ContextHint:   arrearsHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account must send")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["patientKey"].(string); got != patientKey {
		t.Fatalf("params.patientKey = %q, want %q — resolved live off the account's own heldFor link, never the payload", got, patientKey)
	}
}

// TestArrears_NoHeldForPatientStillEvaluates (g2b) is the other half: an
// account that carries no live heldFor patient at all — never bound to one,
// or bound to one that has since gone dead — is still evaluated, because the
// arrears fact is about the ACCOUNT, not the patient it happens to be held
// for. Were the patient a Params column this shape would be UNREACHABLE:
// Weaver's strategist refuses to dispatch any row whose Params reference a
// null column, so this account's gap would simply never open. A past-due
// evaluation on it still sends — with no patientKey in the notification's
// params, because none resolves.
func TestArrears_NoHeldForPatientStillEvaluates(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnopatient")

	acctKey := "vtx.clinicaccount.CLARREARSNPTACCTHJKM"
	seedVertex(t, ctx, conn, acctKey, "clinicaccount", map[string]any{})
	seedEntryAt(t, ctx, conn, acctKey, "CLARREARSNPTTXNAHJKM", "debit", 2500, "2026-06-01T08:00:00Z", "")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "clarrnpteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-06-01T08:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — a patientless account ages from its own history same as any other", got, dueFor(t, "2026-06-01T08:00:00Z"))
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt — a patientless account still gets its one reminder", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account with no patient must still send")
	}
	params, _ := notif["params"].(map[string]any)
	if _, ok := params["patientKey"]; ok {
		t.Fatalf("no patient resolved, so params must carry no patientKey: %+v", params)
	}
}

// TestArrears_LegacyAccountOnlyEverMarksStale (h). An account minted under
// clinic-ledger < 0.3.0 carries no .balance, so a posted entry has no
// before/after balance and cannot tell an episode opening from an episode
// continuing. It may only mark EXISTING state stale — never mint arrears
// state off a number it does not have, which would record a due date computed
// from one entry over a history it never counted.
func TestArrears_LegacyAccountOnlyEverMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearslegacy")

	patientKey := createPatient(t, ctx, conn, cp, cons, "clarrlgcpat000000001", "Riley Chen")
	acctKey := seedLegacyAccount(t, ctx, conn, "CLARREARSLGCACCTHJKM", patientKey)

	// No .arrears yet: a charge against a legacy account mints nothing. Such an
	// account is already opening the never-evaluated gap on its missing
	// evaluatedAt, so there is nothing to record and nothing to lose.
	debitAt(t, ctx, conn, cp, cons, "clarrlgcdebit0000001", acctKey, "2026-08-01T09:00:00Z", 2500)
	if data := arrearsData(t, ctx, conn, acctKey); data != nil {
		t.Fatalf("a legacy account has no balance to reason from, so an entry must mint NO arrears state: %+v", data)
	}

	// Now give it arrears state, as the Weaver-dispatched evaluation would.
	seedAspect(t, ctx, conn, acctKey, "arrears", "clinicAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-22T09:00:00Z",
		"evaluatedAt": "2026-08-22T09:00:00Z",
	})
	debitAt(t, ctx, conn, cp, cons, "clarrlgcdebit0000002", acctKey, "2026-08-25T09:00:00Z", 500)

	data := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := data["stale"].(bool); !stale {
		t.Fatalf("an entry against a legacy account carrying arrears state must mark it stale: %+v", data)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record must be CARRIED, not dropped, or the patient is reminded twice for one debt", got)
	}
	if got, _ := data["remindedFor"].(string); got != "2026-08-16T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the recorded episode carried forward", got)
	}
}

// TestArrears_HistoryPastTheBudgetDegrades (k) is the exhaustion path, and the
// claim is that it is a DEGRADE and not a stop. The replay budget
// (ARREARS_PAGE_LIMIT × ARREARS_MAX_PAGES) is fixed by the Processor's script
// wall, so an account can genuinely outrun it, and the op then cannot name a
// head. A refusal there would be permanent and SILENT: the only thing that
// re-drives this op is the convergence gap the account's own row opens, and a
// rejected op never closes it, so Weaver would re-dispatch the same doomed
// evaluation on every window — no reminder, no error anyone reads, forever.
//
// So the op records the exhaustion instead. It is ACCEPTED, it sends nothing, it
// leaves everything already recorded untouched (a reminder already sent stays
// recorded as sent; a due date already armed is not erased by an evaluation that
// could not read the history), and it drops stale — which the lens pin
// TestClinicArrears_HistoryTooLongGoesQuiet turns into silence. The second half
// is the way back out: the next posted entry drops the flag and re-marks the
// state stale, which re-opens the gap for exactly one more attempt.
func TestArrears_HistoryPastTheBudgetDegrades(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbudget")

	patientKey := createPatient(t, ctx, conn, cp, cons, "clarrbgtpat000000001", "Riley Chen")
	// A LEGACY account (no .balance): that is what makes the second half's
	// DEBIT a stale-marking write. On an account with a balance cache a further
	// charge against an already-owing balance writes nothing at all, so a
	// payment would be the re-arming entry there.
	acctKey := seedLegacyAccount(t, ctx, conn, "CLARREARSBGTACCTHJKM", patientKey)

	// One more posted entry than the budget, so the walk ends with a live
	// cursor and the budget genuinely runs out.
	overBudget := clinicledger.ArrearsPageLimit*clinicledger.ArrearsMaxPages + 1
	for i := 0; i < overBudget; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	// The state a previous, in-budget evaluation left: an episode reminded for.
	seedAspect(t, ctx, conn, acctKey, "arrears", "clinicAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
		"evaluatedAt": "2026-08-17T09:00:00Z",
		"stale":       true,
	})

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "clarrbgteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("an evaluation that could not read the history must send nothing — it does not know the head: %+v", notif)
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if flag, _ := data["historyTooLong"].(bool); !flag {
		t.Fatalf("the exhaustion must be RECORDED, not raised — a refusal is a permanent silent stop: %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("stale asks for a recomputation this op has just attempted; re-asking re-opens the gap the degrade closes: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the evaluation's own submittedAt", got)
	}
	for field, want := range map[string]string{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
	} {
		if got, _ := data[field].(string); got != want {
			t.Fatalf("%s = %q, want %q carried untouched — an evaluation that read nothing must erase nothing", field, got, want)
		}
	}

	// The way back out: one more posted entry, one more attempt.
	debitAt(t, ctx, conn, cp, cons, "clarrbgtdebit0000001", acctKey, "2026-08-25T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if _, ok := after["historyTooLong"]; ok {
		t.Fatalf("a posted entry must drop the flag, or the row stays quiet for the life of the account: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and re-mark the state stale, which is what re-opens the gap for that one attempt: %+v", after)
	}
	if got, _ := after["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record is still carried across the re-arming entry", got)
	}
}

// TestArrears_WrongClassRefused pins the class check on both writers of
// .arrears. This package is the sole writer of that aspect and writes exactly
// clinicAccountArrears; a live document of any other class under the key is a
// fault to refuse (InvalidState), never state to decide a send on or to carry
// forward. The positive half runs first on an identically-shaped document of
// the right class, so the refusal is attributable to the class alone.
func TestArrears_WrongClassRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearswrongclass")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrwcl", "Riley Chen")
	debitAt(t, ctx, conn, cp, cons, "clarrwcldebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)

	// The right class: a payment carries the state forward and the evaluation
	// reads it.
	seedAspect(t, ctx, conn, acctKey, "arrears", "clinicAccountArrears", map[string]any{
		"dueAt": "2026-08-16T09:00:00Z", "evaluatedAt": "2026-08-01T09:00:00Z",
	})
	creditAt(t, ctx, conn, cp, cons, "clarrwclpay000000001", acctKey, "2026-08-02T09:00:00Z", 400, "")
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("positive vector: a partial payment against a well-classed .arrears marks it stale")
	}
	evaluateArrears(t, ctx, conn, cp, cons, "clarrwcleval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-03T09:00:00Z", processor.OutcomeAccepted)

	// The wrong class, same shape.
	seedAspect(t, ctx, conn, acctKey, "arrears", "somethingElse", map[string]any{
		"dueAt": "2026-08-16T09:00:00Z", "evaluatedAt": "2026-08-03T09:00:00Z",
	})
	payEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clarrwclpay000000002"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-04T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100,"memo":"Front-desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}, OptionalReads: []string{acctKey + ".balance"}},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, payEnv,
		"InvalidState: this account's arrears aspect is not a clinicAccountArrears")
	if got := balanceCents(t, ctx, conn, acctKey); got != 600 {
		t.Fatalf("balance after the refused payment = %v, want the untouched 600", got)
	}

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "clarrwcleval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-05T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("want an InvalidState rejection from the evaluation, got %+v", reply.Error)
	}
	doc := readDoc(t, ctx, conn, acctKey+".arrears")
	if cls, _ := doc["class"].(string); cls != "somethingElse" {
		t.Fatalf("a refused evaluation must not rewrite the mis-classed document, class is now %q", cls)
	}
}

// recordArrearsNotification submits one RecordClinicArrearsReminderNotification
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
		OperationType: "RecordClinicArrearsReminderNotification",
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
	if cls, _ := doc["class"].(string); cls != "clinicAccountArrearsNotification" {
		t.Fatalf("%s.arrearsNotification class = %q, want clinicAccountArrearsNotification", acctKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.arrearsNotification carries no data", acctKey)
	}
	return data
}

// TestArrearsNotification_OverwritesAndRefusesForgedRef (l). Two claims about
// the replyOp. First, the write is an OVERWRITE: arrears episodes recur on one
// account and every episode replies onto the same key, so the second episode's
// outcome must replace the first's rather than be rejected as a duplicate.
// Second, externalRef arrives from OUTSIDE the platform — the adapter echoes it
// back through the bridge — and it is the op's only say over which vertex the
// outcome aspect is hung on. Splitting it and trusting the left half means any
// 3-segment vtx key names a target: an externalRef of
// "vtx.identity.<NanoID>:<dueAt>" would write a clinicAccountArrearsNotification
// onto a PATIENT'S LOGIN IDENTITY, a vertex this package has no business
// touching at all. The type check is what stops it. The accepted vectors run
// first, so the refusal is attributable to the type and not to a guard that
// denies every reply — the submissions differ in exactly one segment.
func TestArrearsNotification_OverwritesAndRefusesForgedRef(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnotif")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrntf", "Riley Chen")

	recordArrearsNotification(t, ctx, conn, cp, cons, "clarrntfok0000000001",
		acctKey+":2026-08-16T09:00:00Z", "completed", "2026-08-22T09:00:05Z", processor.OutcomeAccepted)
	outcome := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := outcome["status"].(string); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got, _ := outcome["remindedFor"].(string); got != "2026-08-16T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the dueAt half of the externalRef", got)
	}
	if got, _ := outcome["sentAt"].(string); got != "2026-08-22T09:00:05Z" {
		t.Fatalf("sentAt = %q, want the reply's own submittedAt", got)
	}

	// A second episode's reply on the same account overwrites the first.
	recordArrearsNotification(t, ctx, conn, cp, cons, "clarrntfok0000000002",
		acctKey+":2026-10-01T09:00:00Z", "failed", "2026-10-07T09:00:05Z", processor.OutcomeAccepted)
	outcome = arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := outcome["remindedFor"].(string); got != "2026-10-01T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the SECOND episode's dueAt — a create-only write would have refused it", got)
	}
	if got, _ := outcome["status"].(string); got != "failed" {
		t.Fatalf("status = %q, want the second reply's own verdict", got)
	}

	// The same submission with the account key's TYPE segment swapped.
	victim := "vtx.identity." + ledgerActorID
	reply := recordArrearsNotification(t, ctx, conn, cp, cons, "clarrntfforged000001",
		victim+":2026-08-16T09:00:00Z", "completed", "2026-08-22T09:00:05Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want an InvalidArgument rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "externalRef") {
		t.Fatalf("the refusal must name the field it refused, got %q", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, victim+".arrearsNotification") {
		t.Fatalf("a forged externalRef wrote an aspect onto %s — the op must touch nothing but a clinicaccount", victim)
	}

	// The right type with an id segment that is not a NanoID: the shape and
	// type checks pass, the id grammar refuses, and nothing is written under
	// a key no account could ever carry.
	ghost := "vtx.clinicaccount.zz"
	reply = recordArrearsNotification(t, ctx, conn, cp, cons, "clarrntfghost0000001",
		ghost+":2026-08-16T09:00:00Z", "completed", "2026-08-22T09:00:06Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") || !strings.Contains(reply.Error.Message, "NanoID") {
		t.Fatalf("want an InvalidArgument rejection naming the NanoID grammar, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, ghost+".arrearsNotification") {
		t.Fatalf("a malformed externalRef wrote an aspect under %s", ghost)
	}
}

// arrearsFIFOVector is one shared statement-aging vector: a history, and the
// due date the FIFO must age it to. The vectors below are COPIED FROM
// cmd/clinic-app/ledger_test.go's own deriveStatement vectors — the same
// entries, the same hand-derived statement-level expectations — which is what
// makes TestArrears_FIFOMatchesTheStatement a claim about AGREEMENT rather than
// about this implementation agreeing with itself. The app's tests pin the
// display side against these; this pins the op's side.
type arrearsFIFOVector struct {
	name    string
	acctID  string
	entries []fifoEntry
	wantDue string
}

type fifoEntry struct {
	kind     string
	postedAt string
	cents    int
	reverses int // index of the entry this credit reverses, or -1
}

func arrearsFIFOVectors() []arrearsFIFOVector {
	return []arrearsFIFOVector{
		{
			name: "a single open debit past the term", acctID: "CLAGEACCTAHJKMNPQRST",
			entries: []fifoEntry{
				{"debit", "2026-08-01T00:00:00Z", 4750, -1},
			},
			wantDue: "2026-08-16T00:00:00Z",
		},
		{
			name: "a reversal retires its own debit only", acctID: "CLAGEACCTBHJKMNPQRST",
			entries: []fifoEntry{
				{"debit", "2026-08-01T00:00:00Z", 1000, -1},
				{"debit", "2026-08-20T00:00:00Z", 1000, -1},
				{"credit", "2026-08-21T00:00:00Z", 1000, 1},
			},
			wantDue: "2026-08-16T00:00:00Z",
		},
		{
			// A credit SMALLER than the head debit's remainder: it retires part
			// of that charge and the head does not move.
			name: "a partial payment FIFOs and leaves the remainder", acctID: "CLAGEACCTCHJKMNPQRST",
			entries: []fifoEntry{
				{"debit", "2026-08-01T00:00:00Z", 1000, -1},
				{"debit", "2026-08-20T00:00:00Z", 700, -1},
				{"credit", "2026-08-21T00:00:00Z", 400, -1},
			},
			wantDue: "2026-08-16T00:00:00Z",
		},
		{
			name: "a surplus prepays the next debit", acctID: "CLAGEACCTDHJKMNPQRST",
			entries: []fifoEntry{
				{"credit", "2026-08-01T00:00:00Z", 1000, -1},
				{"debit", "2026-08-02T00:00:00Z", 1000, -1},
				{"debit", "2026-08-28T23:50:00Z", 1425, -1},
			},
			wantDue: "2026-09-12T23:50:00Z",
		},
	}
}

// fifoTxID encodes (vector, entry) as a valid 20-char NanoID so each vector's
// entries carry distinct keys without hand-writing an id per line.
func fifoTxID(v, i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	return "CLAGETXAHJKMNPQRST" + string([]byte{safe[v%len(safe)], safe[i%len(safe)]})
}

// TestArrears_FIFOMatchesTheStatement (i) is the green bar's other half: no
// account is ever reminded for a balance it does not owe, because the op's aging
// and the patient's own statement agree. Each vector's entries are SEEDED
// (rather than posted through the ops) so the exact postedAt values the app's
// vectors specify survive — including the credit-before-any-debit shape the
// surplus vector needs, which a live self-pay would refuse.
func TestArrears_FIFOMatchesTheStatement(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfifo")

	for vi, vec := range arrearsFIFOVectors() {
		patientKey := createPatient(t, ctx, conn, cp, cons, "clarrfifopat"+strconv.Itoa(100+vi), "Vector Patient")
		acctKey := seedLegacyAccount(t, ctx, conn, vec.acctID, patientKey)
		for ei, ent := range vec.entries {
			reverses := ""
			if ent.reverses >= 0 {
				reverses = fifoTxID(vi, ent.reverses)
			}
			seedEntryAt(t, ctx, conn, acctKey, fifoTxID(vi, ei), ent.kind, ent.cents, ent.postedAt, reverses)
		}

		evaluateArrears(t, ctx, conn, cp, cons, "clarrfifoeval"+strconv.Itoa(100+vi),
			bootstrap.WeaverIdentityKey, acctKey, "2026-08-29T00:00:00Z", processor.OutcomeAccepted)

		got, _ := arrearsData(t, ctx, conn, acctKey)["dueAt"].(string)
		if got != vec.wantDue {
			t.Errorf("%s: dueAt = %q, want %q — the op's FIFO must age a history exactly as the patient's statement does", vec.name, got, vec.wantDue)
		}
	}
}

// TestArrears_UndeclaredSubmitterStillHydratesArrears (j) is the guarantee the
// account-side and transaction-side derive_reads both exist for. This envelope
// declares the account root and NOTHING else — the shape a client that never
// read the descriptor sends. .arrears must still be hydrated, because a bare
// update is auto-conditioned only on a key the operation DECLARED (Contract #3
// §3.2), and an undeclared read would be LIVE and its write unconditioned.
//
// The read-drift guard armed on every CapabilityPipeline is the mechanism-level
// assertion: a live, undeclared read of vtx.clinicaccount.<id>.arrears reds this
// test deterministically. The state assertions below are the outcome-level
// residual.
func TestArrears_UndeclaredSubmitterStillHydratesArrears(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsundeclared")

	_, acctKey := openAccount(t, ctx, conn, cp, cons, "clarrund", "Riley Chen")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clarrunddebit0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-01T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		// The account alone. No optionalReads, no .balance, no .arrears.
		ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("the episode must open even when the submitter declared nothing about .arrears")
	}
	wantDue := dueFor(t, "2026-08-01T09:00:00Z")
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q", got, wantDue)
	}

	// And the same for the Weaver-dispatched evaluation, whose own derive_reads
	// declares the key for a dispatcher that omitted it.
	evalEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clarrundeval00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateClinicArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-22T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{acctKey},
			// Both walks stay declared — only a read can be derived server-side.
			Enumerations: []processor.EnumerationHint{
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
				{Hub: acctKey, Relation: "heldFor", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, evalEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := arrearsData(t, ctx, conn, acctKey)["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q — the evaluation must see and rewrite the hydrated aspect", got, wantDue)
	}
}
