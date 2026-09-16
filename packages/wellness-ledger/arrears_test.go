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
// shape a committed entry sits in, with no op run. A non-empty reversesTxID
// additionally seeds a wellnessrefund marker + the settlesRefund link
// (credit -> marker) + the marker's own reverses link (marker -> the
// reversed charge) — the two-hop shape a real refund actually carries
// (WellnessCreditAccount's refundRef leg + wellness-domain's marker), not a
// direct link on the credit the way clinic-ledger's reversesRef is.
func seedEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int, postedAt, reversesTxID string) {
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
	if reversesTxID != "" {
		markerID := "RFND" + txID[4:]
		markerKey := seedRefund(t, ctx, conn, markerID)
		seedLink(t, ctx, conn,
			"lnk.wellnesstransaction."+txID+".settlesRefund.wellnessrefund."+markerID,
			txKey, markerKey, "settlesRefund", "settlesRefund")
		seedLink(t, ctx, conn,
			"lnk.wellnessrefund."+markerID+".reverses.wellnesstransaction."+reversesTxID,
			markerKey, "vtx.wellnesstransaction."+reversesTxID, "reverses", "reverses")
	}
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
// so the new episode's opener posts at an instant EQUAL to the recorded
// sentAt — and it is still the new episode's charge, because the head a send
// was for is a whole net term older than the send. A strict compare would
// carry the old send onto it.
//
// Within one second the FIFO's order is the (postedAt, key) pair, so the two
// entries are seeded with keys that put the payment FIRST: the queue empties
// on the payment and the charge re-opens it, which is what makes the charge
// an opener at all. With the charge sorting first it would join the queue
// behind the still-open Aug 1 head and the payment would then retire that
// head — one continuous episode by the ledger's own order, and the send
// would rightly be carried.
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

	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSTQECRDTAAAA", "credit", 1500, "2026-08-22T09:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSTQEDBTZZZZZ", "debit", 500, "2026-08-22T09:00:00Z", "")

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

// TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode is the moved-head
// boundary: A (Aug 1) is reminded for on Aug 22; B posts Sep 1; on Sep 3 the
// member pays exactly A. The head moves to B, which posted AFTER the recorded
// send — but the account was never square, so this is the same episode and
// the send record must be carried. A boundary read off the head's own
// postedAt would drop it here and send again once B's own term ran out, for
// a debt the member is visibly paying down.
func TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsmovedhead")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSMVHQDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "wlarrmvhacct00000001", identityKey)
	debitAt(t, ctx, conn, cp, cons, "wlarrmvhdebit0000001", acctKey, "2026-08-01T09:00:00Z", 1500)
	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "wlarrmvheval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: A is reminded for past its term")
	}

	debitAt(t, ctx, conn, cp, cons, "wlarrmvhdebit0000002", acctKey, "2026-09-01T09:00:00Z", 1500)
	creditAt(t, ctx, conn, cp, cons, "wlarrmvhpay000000001", acctKey, "2026-09-03T09:00:00Z", 1500, "")

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrmvheval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T10:00:00Z", processor.OutcomeAccepted)
	moved := arrearsData(t, ctx, conn, acctKey)
	wantDue := dueFor(t, "2026-09-01T09:00:00Z")
	if got, _ := moved["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the head moved to B", got, wantDue)
	}
	if got, _ := moved["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the head moved but the account was never square, so the episode's send is carried", got)
	}
	if got, _ := moved["remindedFor"].(string); got != dueFor(t, "2026-08-01T09:00:00Z") {
		t.Fatalf("remindedFor = %q, want A's due date carried (B is not yet due)", got)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("B is not yet due: %+v", notif)
	}

	// B's term runs out. remindedFor moves to B's due date, and NOTHING is
	// sent — one reminder per episode, and this episode already had it.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrmvheval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-20T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("a second reminder for one continuous episode: %+v", notif)
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if got, _ := after["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want B's due date — it is what closes the gap", got)
	}
	if got, _ := after["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the episode's one send carried", got)
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
	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSNLSTXNAHJKM", "debit", 1500, "2026-06-01T08:00:00Z", "")

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
// (ArrearsPageLimit × ArrearsMaxPages) is fixed by the Processor's script
// wall, so an account can genuinely outrun it, and the op then cannot name a
// head. A refusal there would be permanent and SILENT: the only thing that
// re-drives this op is the convergence gap the account's own row opens, and a
// rejected op never closes it, so Weaver would re-dispatch the same doomed
// evaluation on every window — no reminder, no error anyone reads, forever.
//
// The replay is now PAGED, so reaching the degrade costs one dispatch per
// in-budget page first: every page before the last is a checkpoint that
// carries the seeded record untouched and sends nothing, and only the
// dispatch that finds pages > ArrearsMaxPages records the exhaustion. It is
// ACCEPTED, it sends nothing, it leaves everything already recorded untouched
// (a reminder already sent stays recorded as sent; a due date already armed
// is not erased by an evaluation that could not read the history), and it
// drops stale and the checkpoint — which the lens pin
// TestWellnessArrears_HistoryTooLongGoesQuiet turns into silence. The second
// half is the way back out: the next posted entry drops the flag and
// re-marks the state stale, which re-opens the gap for exactly one more
// attempt.
func TestArrears_HistoryPastTheBudgetDegrades(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbudget")

	identityKey := seedIdentity(t, ctx, conn, "WLARREARSBGTQDHJKMNP")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARREARSBGTACCTHJKM", identityKey)

	// One more posted entry than the budget, so the last in-budget page ends
	// with a live cursor and the budget genuinely runs out.
	overBudget := wellnessledger.ArrearsPageLimit*wellnessledger.ArrearsMaxPages + 1
	for i := 0; i < overBudget; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-06-01T08:00:00Z", "")
	}

	// The state a previous, in-budget evaluation left: an episode reminded for.
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
		"evaluatedAt": "2026-08-17T09:00:00Z",
		"stale":       true,
	})
	carried := map[string]string{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
	}

	// Drive one dispatch per page until the degrade lands; every page before
	// it is a checkpoint that carries the record and sends nothing.
	dispatches := 0
	var data map[string]any
	for dispatches < wellnessledger.ArrearsMaxPages+2 {
		dispatches++
		_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrbgteval"+strconv.Itoa(10000000+dispatches),
			bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
		if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
			t.Fatalf("dispatch %d: an evaluation that has not read the whole history must send nothing — it does not know the head: %+v", dispatches, notif)
		}
		data = arrearsData(t, ctx, conn, acctKey)
		for field, want := range carried {
			if got, _ := data[field].(string); got != want {
				t.Fatalf("dispatch %d: %s = %q, want %q carried untouched — a page that has not read the whole history must erase nothing", dispatches, field, got, want)
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
		if got, _ := data["evaluatedAt"].(string); got != "2026-08-17T09:00:00Z" {
			t.Fatalf("dispatch %d: evaluatedAt = %q — a page is not an evaluation, so the last completed one's stamp stands", dispatches, got)
		}
	}
	if dispatches != wellnessledger.ArrearsMaxPages+1 {
		t.Fatalf("the degrade landed on dispatch %d, want %d — every in-budget page is consumed first, then one more dispatch records the exhaustion", dispatches, wellnessledger.ArrearsMaxPages+1)
	}
	if got, _ := data["historyBudget"].(float64); int(got) != wellnessledger.ArrearsPageLimit*wellnessledger.ArrearsMaxPages {
		t.Fatalf("historyBudget = %v, want %d — the budget the flag was recorded under, so a later larger one can tell it apart", data["historyBudget"], wellnessledger.ArrearsPageLimit*wellnessledger.ArrearsMaxPages)
	}
	if _, ok := data["replay"]; ok {
		t.Fatalf("the degrade ends the replay, so the checkpoint must not survive it — a surviving phase would hold a gap open under the flag: %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("stale asks for a recomputation this op has just attempted; re-asking re-opens the gap the degrade closes: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the degrading evaluation's own submittedAt", got)
	}

	// The way back out: one more posted entry, one more attempt.
	debitAt(t, ctx, conn, cp, cons, "wlarrbgtdebit0000001", acctKey, "2026-08-25T09:00:00Z", 500)
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
	if got, _ := after["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record is still carried across the re-arming entry", got)
	}
}

// replayTxID encodes (prefix, i) as a valid 20-char NanoID whose first
// character is `lead`, so a fixture can place an entry on a chosen page: the
// postedTo enumeration pages the SORTED link keys, and the transaction id is
// the first varying segment of lnk.wellnesstransaction.<id>.postedTo…, so ids
// beginning 'A' precede every id beginning 'z'. i is encoded in the last three
// characters over an alphabet that sorts in index order for i < 48.
func replayTxID(lead byte, i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return string(lead) + "WLREPLAYTXAHJKMN" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
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
// checkpoint — phase a, one page, a live cursor, the entries folded so far —
// and nothing else: no notification, no evaluatedAt (this account has never
// been evaluated, and a page is not an evaluation). The second exhausts the
// enumeration, drops the checkpoint, computes the FIFO head over the whole
// entries list and, the head being overdue, sends once. The head is the one a
// single-execution replay over the same entries names: twenty-five charges a
// day apart and six payments of one charge each, interleaved across the two
// pages, pay off the six oldest charges wherever the payments sat in the
// walk, so the head is the seventh charge.
func TestArrears_TwoPageHistoryCompletesInTwoDispatches(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearstwopage")

	identityKey := seedIdentity(t, ctx, conn, "WLARRTW2PGQDHJKMNPQR")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRTW2PGACCTHJKMNP", identityKey)

	// 31 entries in id (= page) order: charges at i = 0..24, payments at the
	// six slots 3, 9, 15, 21, 27, 30 — the last of them on the second page.
	const pageLimit = wellnessledger.ArrearsPageLimit
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
	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrtwoeval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, req1); notif != nil {
		t.Fatalf("a page is not an evaluation — nothing may be sent before the head is known: %+v", notif)
	}
	first := arrearsData(t, ctx, conn, acctKey)
	replay := arrearsReplay(t, first)
	if replay == nil {
		t.Fatalf("a history longer than one page must leave a checkpoint: %+v", first)
	}
	if got, _ := replay["phase"].(string); got != wellnessledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q — the first page opens missing_replay_a", got, wellnessledger.ArrearsPhaseA)
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
		t.Fatalf("replay.entries carries %d rows after the first page, want %d (the whole page, keyed by transaction id)", len(entries), pageLimit)
	}

	// Dispatch 2: the enumeration is exhausted, the head is computed and sent for.
	_, req2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrtwoeval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the finalize page writes no checkpoint: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, wantHead) {
		t.Fatalf("dueAt = %q, want %q — six payments retire the six oldest charges whatever page they sat on, so the seventh charge is the head", got, dueFor(t, wantHead))
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
		t.Fatalf("params.balanceCents = %v, want 1900 (25 × 100 − 6 × 100) — the whole history's balance", got)
	}

	// A re-dispatch over the finished, 31-entry history: the account carries
	// no checkpoint (the finalize page dropped it), so this starts fresh at
	// page 1 — and 31 entries are still two pages, so it leaves a checkpoint
	// again rather than finalizing in one dispatch.
	_, req3 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrtwoeval0000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-24T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, req3) != nil {
		t.Fatal("the episode is already reminded for")
	}
	redispatched := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if redispatched == nil {
		t.Fatal("31 entries are two pages, so a fresh evaluation checkpoints again rather than finalizing in one dispatch")
	}
	if got, _ := redispatched["phase"].(string); got != wellnessledger.ArrearsPhaseA {
		t.Fatalf("a fresh evaluation over a two-page history starts again at phase %q, got %q", wellnessledger.ArrearsPhaseA, got)
	}
}

// TestArrears_ReversalOnAnEarlierPageNetsItsCharge proves the FIFO netting is
// exact ACROSS pages — the reason the checkpoint keeps every entry rather
// than a per-page summary. The reversed charge sits on page 1 and the refund
// credit that reverses it (through this ledger's own settlesRefund ->
// wellnessrefund marker -> reverses walk) on page 2 (ids chosen to sort on
// opposite sides of the page boundary), so no single page's read sees both
// hops in the same dispatch — only the finalize walk, over the assembled
// entries list, does. The oldest charge A is older than the reversed charge
// C: plain FIFO would spend the credit on A and name the first small charge
// as the head; the netting retires C specifically and leaves A — the charge
// the member actually still owes — as the head, the same rule the statement
// runs.
func TestArrears_ReversalOnAnEarlierPageNetsItsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsxpage")

	identityKey := seedIdentity(t, ctx, conn, "WLARRXPGQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRXPGACCTHJKMNPQR", identityKey)

	// Page 1 (30 entries, ids A…, B…, C…): charge A, 28 small charges B, the
	// charge C the reversal names. Page 2 (1 entry, id z…): the reversal.
	chargeA := replayTxID('A', 0)
	chargeC := replayTxID('C', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeA, "debit", 1000, "2026-05-01T12:00:00Z", "")
	for i := 0; i < wellnessledger.ArrearsPageLimit-2; i++ {
		postedAt := time.Date(2026, 6, 1+i, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, postedAt, "")
	}
	seedEntryAt(t, ctx, conn, acctKey, chargeC, "debit", 1000, "2026-07-01T12:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, replayTxID('z', 0), "credit", 1000, "2026-07-02T12:00:00Z", chargeC)

	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrxpgeval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsData(t, ctx, conn, acctKey)
	replay := arrearsReplay(t, first)
	if replay == nil {
		t.Fatalf("fixture: 31 entries must leave a checkpoint after page 1: %+v", first)
	}
	entries, _ := replay["entries"].(map[string]any)
	if len(entries) != wellnessledger.ArrearsPageLimit {
		t.Fatalf("fixture: page 1 must carry exactly one page of entries: got %d", len(entries))
	}
	if arrearsNotification(t, ctx, conn, req1) != nil {
		t.Fatal("nothing is sent mid-replay")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrxpgeval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("two pages, two dispatches: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, "2026-05-01T12:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the reversal on page 2 retires charge C on page 1, leaving A as the head; plain FIFO would have spent it on A", got, dueFor(t, "2026-05-01T12:00:00Z"))
	}
}

// TestArrears_ExactRetirementAcrossPageBoundaryKeepsSentAt proves
// arrears_head's EPISODE START tracking — not just its head/balance — is
// exact ACROSS pages, the reason the checkpoint keeps every entry's own
// postedAt instead of collapsing credits into a running total (see
// arrears_entries' doc comment). It is the paged form of
// TestArrears_ExactRetirementMovesTheHeadWithinTheEpisode: charge A is
// reminded for, charge B posts later and queues behind it, and a PLAIN
// credit — not a netting reversal — exactly retires A, moving the head to B
// while the account was never square in between. Here the retiring credit
// sits alone on page 2 and A sits on page 1, so the finalize walk only sees
// both in the same chronological order a whole-history execution would if
// (and only if) the checkpoint preserved each entry's own postedAt. A
// checkpoint that collapsed credits into one sum sorted as paid before every
// debit (the shape this ledger's own arrears_head cannot tolerate) would
// retroactively "prepay" A and rename B as the episode's opener, dropping
// sentAt — exactly the regression this vector pins.
func TestArrears_ExactRetirementAcrossPageBoundaryKeepsSentAt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsxpageretire")

	identityKey := seedIdentity(t, ctx, conn, "WLARRXRTQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRXRTACCTHJKMNPQR", identityKey)

	chargeA := replayTxID('A', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeA, "debit", 1000, "2026-08-01T09:00:00Z", "")

	// 14 filler debit/credit pairs, all dated well before A and netting to
	// zero exactly — each pair opens and immediately re-empties the queue
	// long before A ever arrives, so none of them touches episode_start.
	// Present purely to fill page 1 up to the limit, so the entries that
	// matter land on the pages this vector is named for.
	for i := 0; i < 14; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('F', i), "debit", 50, "2026-01-01T00:00:00Z", "")
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('F', i+14), "credit", 50, "2026-01-01T00:00:00Z", "")
	}

	// One page (1 + 28 = 29 entries): the pre-existing episode is reminded
	// for, BEFORE B or the retiring credit exist.
	_, sendReq := evaluateArrears(t, ctx, conn, cp, cons, "wlarrxrteval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, sendReq) == nil {
		t.Fatal("fixture precondition: A is reminded for past its term")
	}
	if arrearsReplay(t, arrearsData(t, ctx, conn, acctKey)) != nil {
		t.Fatal("fixture precondition: 29 entries fit in one page")
	}

	// chargeB sorts onto page 1's 30th slot; the credit that exactly
	// retires A sorts alone onto page 2 — the shape the vector is named for.
	chargeB := replayTxID('Y', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeB, "debit", 1000, "2026-09-01T09:00:00Z", "")
	payoffA := replayTxID('z', 0)
	seedEntryAt(t, ctx, conn, acctKey, payoffA, "credit", 1000, "2026-09-03T09:00:00Z", "")

	// Dispatch 1: page 1 (30 entries: A, the 28 fillers, B) — mid-replay.
	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrxrteval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T10:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, req1) != nil {
		t.Fatal("a page sends nothing")
	}
	mid := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, mid) == nil {
		t.Fatalf("31 entries must leave a checkpoint after page 1: %+v", mid)
	}

	// Dispatch 2: page 2 (the lone retiring credit) — finalizes.
	_, req2 := evaluateArrears(t, ctx, conn, cp, cons, "wlarrxrteval0000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-03T11:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("two pages, two dispatches: %+v", final)
	}
	wantDue := dueFor(t, "2026-09-01T09:00:00Z")
	if got, _ := final["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the exact retirement (split across the page boundary) moves the head to B", got, wantDue)
	}
	if got, _ := final["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the head moved but the account was never square, so the episode's send (from BEFORE the page split) must be carried, not dropped as if B had opened a fresh episode", got)
	}
	if got, _ := final["remindedFor"].(string); got != dueFor(t, "2026-08-01T09:00:00Z") {
		t.Fatalf("remindedFor = %q, want A's due date carried (B is not yet due)", got)
	}
	if notif := arrearsNotification(t, ctx, conn, req2); notif != nil {
		t.Fatalf("B is not yet due, and the episode was already reminded for: %+v", notif)
	}
}

// TestArrears_PostedEntryMidReplayRestartsIt pins the checkpoint's reset
// boundary. A posted entry changes the set the cursor pages over, so a
// checkpoint taken before it no longer describes a prefix of the history:
// post_entry drops it on its one branch (mark stale; carry every other
// field) and marks the state stale, the phase gap closes, missing_evaluation
// re-opens, and the next evaluation starts again at page 1 — never resumes a
// cursor over a set that has moved under it. Unlike clinic-ledger there is no
// second, otherwise-no-write post_entry shape to pin separately: every
// posted entry against this ledger takes the same branch, checkpoint or not.
func TestArrears_PostedEntryMidReplayRestartsIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrestart")

	identityKey := seedIdentity(t, ctx, conn, "WLARRRSTQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRRSTACCTHJKMNPQR", identityKey)
	for i := 0; i <= wellnessledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrrsteval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsReplay(t, arrearsData(t, ctx, conn, acctKey)) == nil {
		t.Fatal("fixture: the first dispatch must leave a checkpoint")
	}

	debitAt(t, ctx, conn, cp, cons, "wlarrrstdebit0000001", acctKey, "2026-08-25T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, after) != nil {
		t.Fatalf("a posted entry must drop the checkpoint — the set under its cursor has changed: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and mark the state stale, which is what re-opens the evaluation gap: %+v", after)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrrsteval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-26T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("32 entries are two pages, so the restarted evaluation checkpoints again")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — the evaluation restarts at page 1, it does not resume a cursor over a moved set", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != wellnessledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, wellnessledger.ArrearsPhaseA)
	}
}

// TestArrears_MalformedCheckpointRestartsAtPageOne pins the op's side of the
// hardening the lens pin TestWellnessArrears_MalformedCheckpointReopensEvaluation
// covers: a recorded checkpoint the op cannot resume — an unknown phase here —
// is treated as absent, so the evaluation the re-opened gap dispatches starts
// at page 1 over a fresh entries dict and records a well-formed checkpoint in
// its place, rather than folding onto a corrupt one or refusing.
func TestArrears_MalformedCheckpointRestartsAtPageOne(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbadcheckpoint")

	identityKey := seedIdentity(t, ctx, conn, "WLARRBADQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRBADACCTHJKMNPQR", identityKey)
	for i := 0; i <= wellnessledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", map[string]any{
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
		"replay": map[string]any{
			"phase":  "x",
			"cursor": "lnk.wellnesstransaction." + budgetTxID(5) + ".postedTo.wellnessaccount.WLARRBADACCTHJKMNPQR",
			"pages":  7,
			"entries": map[string]any{
				budgetTxID(0): map[string]any{"postedAt": "2026-01-01T00:00:00Z", "type": "debit", "amountCents": 999999, "reversesKey": nil},
			},
		},
	})

	evaluateArrears(t, ctx, conn, cp, cons, "wlarrbadeval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("31 entries are two pages, so the restarted evaluation checkpoints")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — an unresumable checkpoint is treated as absent, not continued from", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != wellnessledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, wellnessledger.ArrearsPhaseA)
	}
	entries, _ := replay["entries"].(map[string]any)
	if len(entries) != wellnessledger.ArrearsPageLimit {
		t.Fatalf("replay.entries carries %d rows, want %d — a fresh dict over page 1, not the corrupt one folded onto", len(entries), wellnessledger.ArrearsPageLimit)
	}
	first, _ := entries[budgetTxID(0)].(map[string]any)
	if first == nil || first["amountCents"].(float64) != 100 {
		t.Fatalf("the corrupt checkpoint's entry must be replaced by the page's own reading: %v", entries[budgetTxID(0)])
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

	identityKey := seedIdentity(t, ctx, conn, "WLARRADVQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRADVACCTHJKMNPQR", identityKey)
	for i := 0; i < 2*wellnessledger.ArrearsPageLimit+1; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarradveval0000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if first == nil {
		t.Fatal("fixture: page 1 must checkpoint")
	}
	if pages, _ := first["pages"].(float64); pages != 1 {
		t.Fatalf("after one dispatch replay.pages = %v, want 1", first["pages"])
	}
	if phase, _ := first["phase"].(string); phase != wellnessledger.ArrearsPhaseA {
		t.Fatalf("after one dispatch replay.phase = %q, want %q", phase, wellnessledger.ArrearsPhaseA)
	}
	firstCursor, _ := first["cursor"].(string)

	evaluateArrears(t, ctx, conn, cp, cons, "wlarradveval0000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	second := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if second == nil {
		t.Fatal("61 entries are three pages, so the second dispatch is still mid-replay")
	}
	if pages, _ := second["pages"].(float64); pages != 2 {
		t.Fatalf("after two dispatches replay.pages = %v, want 2 — a redelivered dispatch advances, it never repeats the page", second["pages"])
	}
	if phase, _ := second["phase"].(string); phase != wellnessledger.ArrearsPhaseB {
		t.Fatalf("after two dispatches replay.phase = %q, want %q — the flip is what closes missing_replay_a and opens missing_replay_b", phase, wellnessledger.ArrearsPhaseB)
	}
	if cursor, _ := second["cursor"].(string); cursor == "" || cursor <= firstCursor {
		t.Fatalf("the cursor must advance past page 1's (%q), got %q", firstCursor, cursor)
	}
	if entries, _ := second["entries"].(map[string]any); len(entries) != 2*wellnessledger.ArrearsPageLimit {
		t.Fatalf("replay.entries carries %d rows after two pages, want %d", len(entries), 2*wellnessledger.ArrearsPageLimit)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "wlarradveval0000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the third page exhausts the enumeration and finalizes: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, "2026-05-01T12:00:00Z") {
		t.Fatalf("dueAt = %q, want %q over all 61 charges", got, dueFor(t, "2026-05-01T12:00:00Z"))
	}
}

// TestArrears_MidReplayCarriesTheEpisodeRecord pins what a page WRITES: the
// checkpoint, and every other recorded field verbatim. A page has evaluated
// nothing, so the episode's due date, the head it was reminded for, the send
// record and the stale mark all still describe the account exactly as the
// last completed evaluation or posted entry left them — dropping the send
// record here would send twice for one debt once the finalize page found the
// head overdue — and evaluatedAt keeps naming that last completed
// evaluation. Nothing is sent.
func TestArrears_MidReplayCarriesTheEpisodeRecord(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscarry")

	identityKey := seedIdentity(t, ctx, conn, "WLARRCRYQDHJKMNPQRST")
	acctKey := seedAccountHeldFor(t, ctx, conn, "WLARRCRYACCTHJKMNPQR", identityKey)
	for i := 0; i <= wellnessledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seeded := map[string]any{
		"dueAt":       "2026-05-16T12:00:00Z",
		"remindedFor": "2026-05-16T12:00:00Z",
		"sentAt":      "2026-05-17T09:00:00Z",
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", seeded)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "wlarrcryeval0000001",
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
	seedEntryAt(t, ctx, conn, acctKey, "WLARREARSUNDTXNAHJKM", "debit", 1500, "2026-08-01T09:00:00Z", "")
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
