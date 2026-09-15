package wellnessledger_test

// The arrears reminder, end to end through the real Processor pipeline: the
// .arrears episode aspect EvaluateWellnessArrears mints and rewrites, the
// stale mark every posted entry adds, the one-notification-per-EPISODE
// guarantee, the actor guard, the identity resolution, the replay-budget
// degrade, and the bridge's replyOp. Mirrors cafe-ledger's TestArrears_* set
// with the one structural difference this ledger has: it stores NO balance,
// so a posted entry never opens or ends an episode itself — it marks what
// exists stale and the evaluation is the only writer that names a head or
// finds none.

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
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
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
			{OperationType: "EvaluateWellnessArrears", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// arrearsHint is the contextHint the wellnessArrearsReminders playbook
// dispatches with (targets.go): the account root, its absence-tolerant
// .arrears aspect, and the bounded postedTo replay + the heldFor identity
// walk. The per-transaction .entry reads and the per-credit settlesRefund →
// reverses hops that replay discovers are NOT declared — their keys are
// data-derived, the class-(e) split.
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

// evaluateArrears drives one EvaluateWellnessArrears as `actor` at
// `submittedAt`, asserts the outcome, and returns the reply (for a refusal's
// message) and the request id (for the outbox the notification rides on).
// Class is LEFT EMPTY, exactly as Weaver's actuator dispatches a directOp — it
// relies on the Processor's operationType→class reverse index, which resolves
// to the wellnessaccount vertexType handler.
func evaluateArrears(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actor, acctKey, submittedAt string,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateWellnessArrears",
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
	if cls, _ := doc["class"].(string); cls != "wellnessAccountArrears" {
		t.Fatalf("%s.arrears class = %q, want wellnessAccountArrears", acctKey, cls)
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
// WHEN a charge posted. Declares the account alone, exactly as every existing
// dispatcher of this op does; the .arrears key is the DDL's own derive_reads'
// to supply.
func debitAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Class price"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}, OptionalReads: []string{acctKey + ".arrears"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.wellnesstransaction." + nanoIDFromRequestID(reqID)
}

// creditAt is debitAt's payment counterpart. A non-empty refundKey posts the
// credit as a refund settling that wellnessrefund marker (reason "refund" +
// the settlesRefund link), the shape the wellnessRefundSettlement playbook
// dispatches.
func creditAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int, refundKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload := `{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Front-desk payment"}`
	reads := []string{acctKey}
	if refundKey != "" {
		payload = `{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"refundRef":"` + refundKey + `","reason":"refund"}`
		reads = append(reads, refundKey)
	}
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: []string{acctKey + ".arrears"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.wellnesstransaction." + nanoIDFromRequestID(reqID)
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

// seedLink writes one raw link document (both endpoint keys, class and local
// name), the way the fixtures for other packages' link writers do.
func seedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, class, localName string) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"sourceVertex": source, "targetVertex": target,
		"localName": localName, "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

// seedEntryAt seeds one already-posted transaction on acctKey at an EXPLICIT
// postedAt — the vertex, its .entry aspect and the postedTo link back — the
// shape a committed entry sits in, with no op run.
func seedEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int, postedAt string) {
	t.Helper()
	txKey := "vtx.wellnesstransaction." + txID
	acctID := acctKey[len("vtx.wellnessaccount."):]
	doc := map[string]any{"class": "wellnesstransaction", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, txKey, b); err != nil {
		t.Fatalf("seed transaction %s: %v", txKey, err)
	}
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": entryType, "amountCents": amountCents, "postedAt": postedAt,
	})
	seedLink(t, ctx, conn,
		"lnk.wellnesstransaction."+txID+".postedTo.wellnessaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
}

// seedAccountHeldFor seeds a wellnessaccount held for an identity directly —
// the shape WellnessCreateAccount commits, without running it.
func seedAccountHeldFor(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, identityKey string) string {
	t.Helper()
	acctKey := "vtx.wellnessaccount." + acctID
	identityID := identityKey[len("vtx.identity."):]
	doc := map[string]any{"class": "wellnessaccount", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, acctKey, b); err != nil {
		t.Fatalf("seed account %s: %v", acctKey, err)
	}
	seedLink(t, ctx, conn,
		"lnk.wellnessaccount."+acctID+".heldFor.identity."+identityID,
		acctKey, identityKey, "heldFor", "heldFor")
	return acctKey
}

// dueFor is a charge's postedAt plus the package's net term — the same
// arithmetic the member's own statement runs, from the package's constant
// rather than a second literal.
func dueFor(t *testing.T, postedAt string) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339, postedAt)
	if err != nil {
		t.Fatalf("parse %q: %v", postedAt, err)
	}
	return at.AddDate(0, 0, wellnessledger.ArrearsGraceDays).Format(time.RFC3339)
}

// TestArrears_FirstChargeMintsNothingAndEvaluationOpensTheEpisode (a). A
// charge against an account with no arrears state mints NOTHING: with no
// stored balance the entry cannot know it is the head, and such an account
// is already opening the never-evaluated gap. The evaluation is what opens
// the episode — it replays the history, finds the charge open, and stamps
// dueAt = that charge's own postedAt plus the net term; not yet due, so no
// remindedFor, no sentAt, nothing sent.
func TestArrears_FirstChargeMintsNothingAndEvaluationOpensTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsopen")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSQPENQDHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarropenacct0000001", identityKey)
	if arrearsData(t, ctx, conn, acctKey) != nil {
		t.Fatal("WellnessCreateAccount must mint NO .arrears — a new account owes nothing, and its missing evaluatedAt is what opens the gap once")
	}

	debitAt(t, ctx, conn, cp, cons, "wlarropendebit000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	if data := arrearsData(t, ctx, conn, acctKey); data != nil {
		t.Fatalf("a charge against an account with no arrears state must mint nothing — the never-evaluated gap does the first evaluation: %+v", data)
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarropeneval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-05T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("the evaluation must write .arrears — its evaluatedAt is what closes the never-evaluated gap")
	}
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-01T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q (the open charge's own postedAt + the net term)", got, dueFor(t, "2026-08-01T09:00:00Z"))
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-05T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the evaluation's own submittedAt", got)
	}
	for _, absent := range []string{"remindedFor", "sentAt", "stale", "historyTooLong"} {
		if _, ok := data[absent]; ok {
			t.Fatalf("a head that is not yet due carries no %s: %+v", absent, data)
		}
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a head that is not yet due must send nothing, got %+v", notif)
	}
}

// TestArrears_SecondChargeLeavesTheHead (b). The head is the OLDEST open
// charge, and a second charge queues behind it. This ledger's post_entry
// cannot see that (no balance), so it marks the recorded state stale and
// carries dueAt untouched; the recomputation the stale mark asks for then
// finds the same head and clears the mark — never re-stamping the due date of
// a weeks-old debt to the day of the newest class.
func TestArrears_SecondChargeLeavesTheHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssecond")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSSNDQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrsndacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrsnddebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrsndeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-05T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsData(t, ctx, conn, acctKey)

	debitAt(t, ctx, conn, cp, cons, "wlarrsnddebit0000002", acctKey, "2026-08-20T09:00:00Z", 500)

	marked := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := marked["stale"].(bool); !stale {
		t.Fatalf("a posted entry must mark the recorded arrears state stale — it has no balance to reason from: %+v", marked)
	}
	if marked["dueAt"] != first["dueAt"] {
		t.Fatalf("the stale mark is an ADDITION, not a rewrite: dueAt moved from %v to %v", first["dueAt"], marked["dueAt"])
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrsndeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-21T09:00:00Z", processor.OutcomeAccepted)
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

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSSTLQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrstlacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrstldebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "wlarrstldebit0000002", acctKey, "2026-08-20T09:00:00Z", 500)
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrstleval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-20T10:00:00Z", processor.OutcomeAccepted)
	before := arrearsData(t, ctx, conn, acctKey)
	if _, ok := before["stale"]; ok {
		t.Fatalf("fixture precondition: a fresh evaluation carries no stale mark: %+v", before)
	}

	creditAt(t, ctx, conn, cp, cons, "wlarrstlpay000000001", acctKey, "2026-08-21T09:00:00Z", 1000, "")

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
// .arrears to {evaluatedAt} alone: no dueAt, so no timer stays armed, and
// nothing of the finished episode survives — which is what lets the NEXT
// charge open a clean episode and be reminded for on its own merits.
func TestArrears_PaymentToZeroEndsTheEpisodeOnEvaluation(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscleared")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSCLRQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrclracct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrclrdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrclreval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; !ok {
		t.Fatal("fixture precondition: the overdue head must have been reminded for")
	}

	creditAt(t, ctx, conn, cp, cons, "wlarrclrpay000000001", acctKey, "2026-08-25T09:00:00Z", 1500, "")
	marked := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := marked["stale"].(bool); !stale {
		t.Fatalf("a clearing payment cannot be told from a partial one at the entry — it must mark stale: %+v", marked)
	}
	if got, _ := marked["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the entry must CARRY the send record; only the evaluation may drop it", got)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrclreval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-26T09:00:00Z", processor.OutcomeAccepted)
	cleared := arrearsData(t, ctx, conn, acctKey)
	if got, _ := cleared["evaluatedAt"].(string); got != "2026-08-26T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the evaluation's own submittedAt", got)
	}
	for _, absent := range []string{"dueAt", "remindedFor", "sentAt", "stale", "historyTooLong"} {
		if _, ok := cleared[absent]; ok {
			t.Fatalf("a paid-off account keeps no %s — the episode is over and {evaluatedAt} alone remains: %+v", absent, cleared)
		}
	}

	// A NEW charge, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "wlarrclrdebit0000002", acctKey, "2026-08-29T09:00:00Z", 500)
	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrclreval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID) == nil {
		t.Fatal("a NEW episode past its term must send — one per episode is not one per account")
	}
	reopened := arrearsData(t, ctx, conn, acctKey)
	if got, _ := reopened["dueAt"].(string); got != dueFor(t, "2026-08-29T09:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the new episode's head is the new charge", got, dueFor(t, "2026-08-29T09:00:00Z"))
	}
}

// TestArrears_NewEpisodeBeforeEvaluationDropsTheOldSend (d2) is the episode
// boundary this ledger has to find at evaluation time, because nothing ends
// an episode at the entry: a payment to zero and a fresh charge both post
// before the evaluation runs. The recorded send (Aug 22) was for the Aug 1
// charge, which the Aug 25 payment retired; the Aug 25 charge opens a NEW
// episode, and its head posted after the send. The evaluation must drop the
// finished episode's remindedFor/sentAt — carrying them would leave a
// CreditHold standing on a not-yet-due charge nobody was reminded for, and
// when that charge fell due, remindedFor would be stamped with no
// notification ever going out for it.
func TestArrears_NewEpisodeBeforeEvaluationDropsTheOldSend(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsboundary")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSBNDQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrbndacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrbnddebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrbndeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if got, _ := arrearsData(t, ctx, conn, acctKey)["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("fixture precondition: the Aug 1 charge must have been reminded for, sentAt = %q", got)
	}

	// Paid off, then charged again, with no evaluation in between.
	creditAt(t, ctx, conn, cp, cons, "wlarrbndpay000000001", acctKey, "2026-08-25T09:00:00Z", 1500, "")
	debitAt(t, ctx, conn, cp, cons, "wlarrbnddebit0000002", acctKey, "2026-08-25T09:05:00Z", 500)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrbndeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-25T09:10:00Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	wantDue := dueFor(t, "2026-08-25T09:05:00Z")
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the new charge is the new episode's head", got, wantDue)
	}
	if _, ok := data["sentAt"]; ok {
		t.Fatalf("the finished episode's send record must not survive into the new one — a hold would stand on a charge nobody was reminded for: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("remindedFor travels with sentAt and must go with it: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("the new head is not yet due, so nothing goes out: %+v", notif)
	}

	// The new episode falls due and is reminded for on its own merits.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrbndeval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID2) == nil {
		t.Fatal("the new episode past its term must send — the old episode's send does not count for it")
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if got, _ := after["sentAt"].(string); got != "2026-09-14T09:00:00Z" {
		t.Fatalf("sentAt = %q, want this evaluation's own instant", got)
	}
	if got, _ := after["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q", got, wantDue)
	}
}

// TestArrears_NewEpisodeInTheSendsOwnSecondDropsTheOldSend is the tie rule:
// the payment to zero and the fresh charge post in the very second the
// reminder went out. postedAt and sentAt are both whole-second canonical UTC,
// so the new head's postedAt EQUALS the recorded sentAt — and it is still the
// new episode's charge, because the head a send was for is a whole net term
// older than the send. A strict compare would carry the old send onto it.
func TestArrears_NewEpisodeInTheSendsOwnSecondDropsTheOldSend(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearstie")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSTQEQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrtieacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrtiedebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "wlarrtieeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: the Aug 1 charge must have been reminded for")
	}

	creditAt(t, ctx, conn, cp, cons, "wlarrtiepay000000001", acctKey, "2026-08-22T09:00:00Z", 1500, "")
	debitAt(t, ctx, conn, cp, cons, "wlarrtiedebit0000002", acctKey, "2026-08-22T09:00:00Z", 500)

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrtieeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:01Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != dueFor(t, "2026-08-22T09:00:00Z") {
		t.Fatalf("dueAt = %q, want the new charge's term", got)
	}
	if _, ok := data["sentAt"]; ok {
		t.Fatalf("a head posted in the send's own second is the NEXT episode's — the send cannot have been for it: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("remindedFor goes with sentAt: %+v", data)
	}
}

// TestArrears_PartialPaymentThenChargeKeepsTheSend is the boundary vector's
// positive sibling: the same sequence with a payment that leaves the balance
// open. The head stays the Aug 1 charge (partially paid), the episode never
// ended, and the evaluation must CARRY the send record — the member is still
// in the debt they were reminded of, and dropping the record would send
// again for it.
func TestArrears_PartialPaymentThenChargeKeepsTheSend(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscontinues")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSCNTQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrcntacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrcntdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrcnteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	creditAt(t, ctx, conn, cp, cons, "wlarrcntpay000000001", acctKey, "2026-08-25T09:00:00Z", 1000, "")
	debitAt(t, ctx, conn, cp, cons, "wlarrcntdebit0000002", acctKey, "2026-08-25T09:05:00Z", 500)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrcnteval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-25T09:10:00Z", processor.OutcomeAccepted)
	data := arrearsData(t, ctx, conn, acctKey)
	wantDue := dueFor(t, "2026-08-01T09:00:00Z")
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the partially paid Aug 1 charge is still the head", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the episode continues, so its send record is carried", got)
	}
	if got, _ := data["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q carried", got, wantDue)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("an episode already reminded for sends nothing on a re-evaluation: %+v", notif)
	}
}

// TestArrears_EvaluateSendsOnceThenNothing (e) is the green bar: a member
// past the net term is reminded ONCE per episode. The first evaluation stamps
// remindedFor + sentAt and emits the external.notification the bridge turns
// into a real message; a re-dispatch recomputes the same head, finds sentAt
// already recorded, and emits nothing at all.
func TestArrears_EvaluateSendsOnceThenNothing(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssend")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSSENDQDHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrsendacct0000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrsenddebit000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	wantDue := dueFor(t, "2026-08-01T09:00:00Z")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrsendeval0000001",
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
	if got, _ := notif["replyOp"].(string); got != "RecordWellnessArrearsReminderNotification" {
		t.Fatalf("replyOp = %q, want RecordWellnessArrearsReminderNotification", got)
	}
	params, _ := notif["params"].(map[string]any)
	if params == nil {
		t.Fatalf("the notification carries no params: %+v", notif)
	}
	if got, _ := params["accountKey"].(string); got != acctKey {
		t.Fatalf("params.accountKey = %q, want %q", got, acctKey)
	}
	if got, _ := params["identityKey"].(string); got != identityKey {
		t.Fatalf("params.identityKey = %q, want %q (resolved live off the account's own heldFor link, not the payload)", got, identityKey)
	}
	if got, _ := params["reminderType"].(string); got != "wellnessArrears" {
		t.Fatalf("params.reminderType = %q, want wellnessArrears", got)
	}
	if got, _ := params["dueAt"].(string); got != wantDue {
		t.Fatalf("params.dueAt = %q, want %q", got, wantDue)
	}
	if got, _ := params["balanceCents"].(float64); got != 1500 {
		t.Fatalf("params.balanceCents = %v, want 1500", got)
	}

	// The re-dispatch. Same account, same history, a later instant: the head
	// is unchanged, sentAt already stands, and NOTHING goes out.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrsendeval0000002",
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

// TestArrears_EvaluateRearmsOnACoveredHead (f). A payment covered the charge
// the recorded due date came from, so the FIFO head is now a LATER charge
// with a later due date. The evaluation recomputes it, clears stale, carries
// no send record (none was made), and the recomputed date re-arms the timer —
// with no notification, because nothing is overdue yet.
func TestArrears_EvaluateRearmsOnACoveredHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrearm")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSRRMQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrrearmacct000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrrearmdebit00001", acctKey, "2026-08-01T09:00:00Z", 1000)
	// The episode opens on the Aug 1 charge (due Aug 16), evaluated before
	// its term so nothing is reminded for yet.
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrrearmeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-05T09:00:00Z", processor.OutcomeAccepted)
	debitAt(t, ctx, conn, cp, cons, "wlarrrearmdebit00002", acctKey, "2026-08-20T09:00:00Z", 500)
	creditAt(t, ctx, conn, cp, cons, "wlarrrearmpay0000001", acctKey, "2026-08-21T09:00:00Z", 1000, "")
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the partial payment must have marked the state stale")
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrrearmeval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	// The Aug 1 charge was fully paid off, so the head is the Aug 20 one.
	wantDue := dueFor(t, "2026-08-20T09:00:00Z")
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the FIFO head moved to the surviving charge", got, wantDue)
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

// TestArrears_EvaluateNetsARefundAgainstItsCharge proves arrears_head's
// netting pre-pass matches the statement's own rule (cmd/wellness-app/
// ledger.go deriveStatement), not plain FIFO — and that it finds the charge
// through the two hops this ledger's refund actually carries: the refund
// credit's settlesRefund link to the wellnessrefund marker, and the marker's
// reverses link to the charge. The refund of the NEWER charge B posts as an
// ordinary credit whose amount, under plain FIFO, would retire the OLDER
// charge A first and leave B as the head. The netting reads reversesKey and
// retires B specifically, so A — untouched — is the head, and the dueAt
// asserted below is the netting rule's answer; no FIFO application of these
// rows produces it.
func TestArrears_EvaluateNetsARefundAgainstItsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnetref")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSNETQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrnetacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrnetdebita000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	chargeB := debitAt(t, ctx, conn, cp, cons, "wlarrnetdebitb000001", acctKey, "2026-08-20T09:00:00Z", 1000)

	// The marker wellness-domain's CancelBooking would have minted for B, with
	// its reverses link to B — the shape the refund credit settles.
	refundKey := seedRefund(t, ctx, conn, "WLARRNETREFUNDHJKMNP")
	refundID := refundKey[len("vtx.wellnessrefund."):]
	chargeBID := chargeB[len("vtx.wellnesstransaction."):]
	seedLink(t, ctx, conn,
		"lnk.wellnessrefund."+refundID+".reverses.wellnesstransaction."+chargeBID,
		refundKey, chargeB, "reverses", "reverses")
	creditAt(t, ctx, conn, cp, cons, "wlarrnetrefund000001", acctKey, "2026-08-21T09:00:00Z", 1000, refundKey)

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrneteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	wantDue := dueFor(t, "2026-08-01T09:00:00Z")
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the reversal retires B directly, leaving A (Aug 1) as the head", got, wantDue)
	}
}

// TestArrears_OneNotificationPerEpisodeNotPerHead (f2) is the once-per-episode
// guarantee at its only hard case. An EPISODE is the stretch from the charge
// that takes the account from square to owing until the balance comes back
// to zero; the FIFO HEAD moves within one episode every time a partial
// payment retires the oldest charge. A member who pays SOMETHING off is doing
// the right thing, and if the send were keyed on the head — on remindedFor
// naming this dueAt — every part-payment would hand them a second nag for the
// same continuous debt, because the charge the head moves to is usually past
// its own term too. The send is keyed on sentAt's ABSENCE instead, which is a
// fact about the episode. remindedFor is still written every time, because
// that is what closes the convergence gap; the two are deliberately different
// questions.
func TestArrears_OneNotificationPerEpisodeNotPerHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsepisode")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSEPSQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrepsacct00000001", identityKey)

	debitAt(t, ctx, conn, cp, cons, "wlarrepsdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "wlarrepsdebit0000002", acctKey, "2026-08-10T09:00:00Z", 1000)
	dueC1 := dueFor(t, "2026-08-01T09:00:00Z")
	dueC2 := dueFor(t, "2026-08-10T09:00:00Z")

	// The first charge falls due and the reminder goes out.
	_, reqID1 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrepseval00000001",
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

	// A PARTIAL payment retires the first charge exactly. The head moves to
	// the second, whose own term has also passed — and nothing may go out for
	// it.
	creditAt(t, ctx, conn, cp, cons, "wlarrepspay000000001", acctKey, "2026-08-18T09:00:00Z", 1000, "")
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: a posted entry marks the state stale")
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrepseval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-27T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("the head moved WITHIN one episode — a member paying their balance down must not be nagged twice: %+v", notif)
	}
	moved := arrearsData(t, ctx, conn, acctKey)
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

	// Paying the balance off ENDS the episode on the next evaluation: the
	// send record goes with it.
	creditAt(t, ctx, conn, cp, cons, "wlarrepspay000000002", acctKey, "2026-08-28T09:00:00Z", 1000, "")
	evaluateArrears(t, ctx, conn, cp, cons, "wlarrepseval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-28T10:00:00Z", processor.OutcomeAccepted)
	cleared := arrearsData(t, ctx, conn, acctKey)
	if _, ok := cleared["sentAt"]; ok {
		t.Fatalf("a paid-off account keeps no send record — the NEXT episode must be able to send: %+v", cleared)
	}
	if _, ok := cleared["dueAt"]; ok {
		t.Fatalf("a paid-off account carries no dueAt: %+v", cleared)
	}

	// A NEW charge, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "wlarrepsdebit0000003", acctKey, "2026-08-29T09:00:00Z", 500)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; ok {
		t.Fatal("a fresh episode starts with no send record")
	}
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrepseval00000004",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID3) == nil {
		t.Fatal("a NEW episode past its term must send — one per episode is not one per account")
	}
}

// TestArrears_ForgedSendRefused (g) is the actor guard. ledgerActorKey holds
// the operator role AND the identical Scope:"any" EvaluateWellnessArrears
// grant, so step 3 authorizes it; only `op.actor != primordialActor["weaver"]`
// stops it from having the platform tell an arbitrary member they owe money.
// Nothing is written either — a marker minted on a forged send would close
// the gap and suppress the real reminder. A payload carrying sentAt /
// remindedFor is the same forgery through a different door: the op reads
// neither field, so the seeded record it would have to overwrite stands.
func TestArrears_ForgedSendRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsforged")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSFGDQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrfgdacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrfgddebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "wlarrfgdeval00000001",
		ledgerActorKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeRejected)
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
	// nothing about what is stamped — the head is not yet due at this
	// instant, so neither field may appear.
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wlarrfgdeval00000002"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateWellnessArrears",
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
}

// TestArrears_IdentityResolvedFromAccountState (g2a). identityKey never
// travels through the payload — a Weaver Params reference to an optional
// column would make the strategist refuse to dispatch any row where the hop
// misses, silently starving the very accounts most worth aging. The op
// resolves the identity itself, live, off the account's own heldFor
// out-link, so it decides this with no payload field to trust or forge; a
// payload identityKey naming a stranger is ignored.
func TestArrears_IdentityResolvedFromAccountState(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsidstate")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSQDSQDHJKMNP")
	stranger := seedIdentity(t, ctx, conn, "WLARREARSSTRANGERHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarridsacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarridsdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)

	reqID := testutil.GenReqID("wlarridseval00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateWellnessArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-22T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","identityKey":"` + stranger + `"}`),
		ContextHint:   arrearsHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account must send")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["identityKey"].(string); got != identityKey {
		t.Fatalf("params.identityKey = %q, want %q — resolved live off the account's own heldFor link, never the payload", got, identityKey)
	}
}

// TestArrears_NoHeldForIdentityStillEvaluates (g2b) is the other half: an
// account that carries no live heldFor identity at all — never bound to one,
// or bound to one that has since gone dead — is still evaluated, because the
// arrears fact is about the ACCOUNT, not the member it happens to be held
// for. Were the identity a Params column this shape would be UNREACHABLE:
// Weaver's strategist refuses to dispatch any row whose Params reference a
// null column, so this account's gap would simply never open. A past-due
// evaluation on it still sends — with no identityKey in the notification's
// params, because none resolves.
func TestArrears_NoHeldForIdentityStillEvaluates(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnoidentity")

	acctKey := "vtx.wellnessaccount.WLARREARSNLSACCTHJKM"
	doc := map[string]any{"class": "wellnessaccount", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, acctKey, b); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSNLSTXNAHJKM", "debit", 1500, "2026-06-01T08:00:00Z")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrnlseval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	wantDue := dueFor(t, "2026-06-01T08:00:00Z")
	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — an identity-less account ages from its own history same as any other", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt — an identity-less account still gets its one reminder", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account with no identity must still send")
	}
	params, _ := notif["params"].(map[string]any)
	if _, ok := params["identityKey"]; ok {
		t.Fatalf("no identity resolved, so params must carry no identityKey: %+v", params)
	}
}

// budgetTxID mints the i-th of the 501 transaction ids the budget vector
// seeds — valid 20-char NanoIDs, distinct per i.
func budgetTxID(i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return "WLARREARSBUDGETTX" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
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
// So the op records the exhaustion instead. It is ACCEPTED, it sends nothing,
// it leaves everything already recorded untouched (a reminder already sent
// stays recorded as sent; a due date already armed is not erased by an
// evaluation that could not read the history), and it drops stale — which the
// lens pin TestWellnessArrears_HistoryTooLongGoesQuiet turns into silence. The
// second half is the way back out: the next posted entry drops the flag and
// re-marks the state stale, which re-opens the gap for exactly one more
// attempt.
func TestArrears_HistoryPastTheBudgetDegrades(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbudget")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSBGTQDHJKMNP")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARREARSBGTACCTHJKM", identityKey)

	// 501 posted entries: one more than ARREARS_PAGE_LIMIT × ARREARS_MAX_PAGES,
	// so the walk ends with a live cursor and the budget genuinely runs out.
	const overBudget = 501
	for i := 0; i < overBudget; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-06-01T08:00:00Z")
	}

	// The state a previous, in-budget evaluation left: an episode reminded for.
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
		"evaluatedAt": "2026-08-17T09:00:00Z",
		"stale":       true,
	})

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrbgteval00000001",
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
	debitAt(t, ctx, conn, cp, cons, "wlarrbgtdebit0000001", acctKey, "2026-08-25T09:00:00Z", 500)
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

// TestArrears_WrongClassRefused pins the class check at both writers: a
// .arrears document of any other class is a fault to refuse, never state to
// carry (post_entry) or to decide a send on (the evaluation).
func TestArrears_WrongClassRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearswrongclass")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSWRCQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrwrcacct00000001", identityKey)
	seedAspect(t, ctx, conn, acctKey, "arrears", "somethingElse", map[string]any{"evaluatedAt": "2026-08-01T09:00:00Z"})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wlarrwrcdebit0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-01T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"memo":"Class price"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}, OptionalReads: []string{acctKey + ".arrears"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("a debit over a wrong-class .arrears: outcome = %v error = %+v, want an InvalidState rejection", outcome, reply.Error)
	}

	reply, _ = evaluateArrears(t, ctx, conn, cp, cons, "wlarrwrceval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("an evaluation over a wrong-class .arrears: want an InvalidState rejection, got %+v", reply.Error)
	}
}

// TestArrears_UndeclaredSubmitterStillHydratesArrears is the derive_reads
// proof for all three governed ops: a submitter that declares NOTHING — no
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

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSUNDQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrundacct00000001", identityKey)
	// The charge the seeded send was for: it must predate sentAt, or the
	// evaluation rightly reads the record as a finished episode's and drops it.
	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSUNDTXNAHJKM", "debit", 1500, "2026-08-01T09:00:00Z")
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
		"evaluatedAt": "2026-08-17T09:00:00Z",
	})

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wlarrunddebit0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-20T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"memo":"Class price"}`),
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	afterDebit := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := afterDebit["stale"].(bool); !stale {
		t.Fatalf("the stale mark must land even when the submitter declared nothing about .arrears: %+v", afterDebit)
	}
	if got, _ := afterDebit["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the derived hydration is what lets the entry carry the send record", got)
	}

	creditEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wlarrundcredit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-21T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500}`),
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	afterCredit := arrearsData(t, ctx, conn, acctKey)
	if got, _ := afterCredit["remindedFor"].(string); got != "2026-08-16T09:00:00Z" {
		t.Fatalf("remindedFor = %q — an undeclared credit must carry the record forward too", got)
	}

	// And the Weaver-dispatched evaluation, whose own derive_reads declares the
	// key for a dispatcher that omitted it: it must see the carried sentAt and
	// send NOTHING for this already-reminded episode.
	evalReqID := testutil.GenReqID("wlarrundeval00000001")
	evalEnv := &processor.OperationEnvelope{
		RequestID:     evalReqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateWellnessArrears",
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
	if got, _ := evaluated["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the evaluation must see and carry the hydrated record", got)
	}
	if _, ok := evaluated["stale"]; ok {
		t.Fatalf("the evaluation must clear the stale mark it was dispatched for: %+v", evaluated)
	}
}

// recordArrearsNotification submits one RecordWellnessArrearsReminderNotification
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
		OperationType: "RecordWellnessArrearsReminderNotification",
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
	if cls, _ := doc["class"].(string); cls != "wellnessAccountArrearsNotification" {
		t.Fatalf("%s.arrearsNotification class = %q, want wellnessAccountArrearsNotification", acctKey, cls)
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
// write a wellnessAccountArrearsNotification onto a MEMBER'S IDENTITY, a
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

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSNTFQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrntfacct00000001", identityKey)

	recordArrearsNotification(t, ctx, conn, cp, cons, "wlarrntfok0000000001",
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

	// The same submission with the account key's TYPE segment swapped.
	victim := "vtx.identity." + ledgerActorID
	reply := recordArrearsNotification(t, ctx, conn, cp, cons, "wlarrntfforged000001",
		victim+":2026-08-16T09:00:00Z", "completed", "2026-08-22T09:00:05Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want an InvalidArgument rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "externalRef") {
		t.Fatalf("the refusal must name the field it refused, got %q", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, victim+".arrearsNotification") {
		t.Fatalf("a forged externalRef wrote an aspect onto %s — the op must touch nothing but a wellnessaccount", victim)
	}

	// A later episode replies onto the SAME key and replaces the record —
	// arrears recur, and a create-only write would have refused this.
	recordArrearsNotification(t, ctx, conn, cp, cons, "wlarrntfok0000000002",
		acctKey+":2026-09-20T09:00:00Z", "failed", "2026-09-26T09:00:05Z", processor.OutcomeAccepted)
	second := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := second["remindedFor"].(string); got != "2026-09-20T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the later episode's dueAt — the aspect holds the LATEST episode's outcome", got)
	}
	if got, _ := second["status"].(string); got != "failed" {
		t.Fatalf("status = %q, want the later episode's verdict", got)
	}
}
