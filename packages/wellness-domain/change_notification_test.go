// RecordBookingChangeNotification integration tests for the wellness-domain
// Capability Package — the audit replyOp's write path driven end to end
// through a real Processor, plus the assertion that ReleaseOrphanedBooking
// emits the call-off half of the notice in its own batch (notifications.go,
// ddls.go).
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// cnSubmit dispatches RecordBookingChangeNotification as the operator actor
// (domainCapDoc's grant) with no ContextHint — the bridge submits none, since
// the op reads nothing.
func cnSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, payload string, want processor.MessageOutcome) (string, processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordBookingChangeNotification",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-09-30T15:00:05Z",
		Payload:       json.RawMessage(payload),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reqID, outcome, reply
}

// TestRecordBookingChangeNotification_RecordsOutcomeOnLiveBooking drives the
// op against a live booking with kind=moved, asserting the committed
// .changeNotification aspect matches the split-out kind/changeRef/status and
// the op's own normalized sentAt, plus the recorded event.
func TestRecordBookingChangeNotification_RecordsOutcomeOnLiveBooking(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "changenotifylive")

	bookingKey := "vtx.booking.BBCNAJHKMNPQRSTUVWX1"
	seedVertex(t, ctx, conn, bookingKey, "booking", nil)

	extRef := bookingKey + ":moved:2026-10-01T15:00:00Z"
	reqID, _, _ := cnSubmit(t, ctx, conn, cp, cons, "wdcnlive0000001",
		`{"externalRef":"`+extRef+`","status":"completed","result":"notification sent"}`, processor.OutcomeAccepted)

	notif := readDoc(t, ctx, conn, bookingKey+".changeNotification")
	if notif["class"] != "bookingChangeNotification" {
		t.Fatalf("changeNotification class = %v, want bookingChangeNotification", notif["class"])
	}
	data, _ := notif["data"].(map[string]any)
	if data["kind"] != "moved" {
		t.Fatalf("changeNotification kind = %v, want moved", data["kind"])
	}
	if data["changeRef"] != "2026-10-01T15:00:00Z" {
		t.Fatalf("changeNotification changeRef = %v, want 2026-10-01T15:00:00Z", data["changeRef"])
	}
	if data["status"] != "completed" {
		t.Fatalf("changeNotification status = %v, want completed", data["status"])
	}
	if data["sentAt"] != "2026-09-30T15:00:05Z" {
		t.Fatalf("changeNotification sentAt = %v, want 2026-09-30T15:00:05Z", data["sentAt"])
	}
	assertTrackerEvent(t, ctx, conn, reqID, "wellness.bookingChangeNotificationRecorded")
}

// TestRecordBookingChangeNotification_RecordsOutcomeOnTombstonedBooking is
// the call-off case: the booking is already tombstoned (the same batch that
// emits the call-off notice tombstones it, ddls.go's ReleaseOrphanedBooking
// branch), so the replyOp arrives after the fact. It must still commit — the
// op runs no liveness check by design.
func TestRecordBookingChangeNotification_RecordsOutcomeOnTombstonedBooking(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "changenotifydead")

	bookingKey := "vtx.booking.BBCNDEADAJHKMNPQRST1"
	seedTombstonedVertex(t, ctx, conn, bookingKey, "booking")

	sessionKey := "vtx.session.BBCNSESAJHKMNPQRSTU1"
	extRef := bookingKey + ":calledOff:" + sessionKey
	reqID, _, _ := cnSubmit(t, ctx, conn, cp, cons, "wdcndead0000001",
		`{"externalRef":"`+extRef+`","status":"completed"}`, processor.OutcomeAccepted)

	notif := readDoc(t, ctx, conn, bookingKey+".changeNotification")
	data, _ := notif["data"].(map[string]any)
	if data["kind"] != "calledOff" {
		t.Fatalf("changeNotification kind = %v, want calledOff", data["kind"])
	}
	if data["changeRef"] != sessionKey {
		t.Fatalf("changeNotification changeRef = %v, want %v", data["changeRef"], sessionKey)
	}
	assertTrackerEvent(t, ctx, conn, reqID, "wellness.bookingChangeNotificationRecorded")
}

// TestRecordBookingChangeNotification_SecondReplyOverwrites proves the bare
// upsert: a second, distinct reply for the same booking overwrites the
// first — the latest outcome always wins, unlike the create-only reminder
// notification.
func TestRecordBookingChangeNotification_SecondReplyOverwrites(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "changenotifytwice")

	bookingKey := "vtx.booking.BBCNTWCAJHKMNPQRSTU1"
	seedVertex(t, ctx, conn, bookingKey, "booking", nil)

	firstRef := bookingKey + ":promoted:2026-09-01T07:57:00Z"
	cnSubmit(t, ctx, conn, cp, cons, "wdcntwice0000001",
		`{"externalRef":"`+firstRef+`","status":"completed"}`, processor.OutcomeAccepted)

	secondRef := bookingKey + ":moved:2026-10-01T15:00:00Z"
	cnSubmit(t, ctx, conn, cp, cons, "wdcntwice0000002",
		`{"externalRef":"`+secondRef+`","status":"completed"}`, processor.OutcomeAccepted)

	notif := readDoc(t, ctx, conn, bookingKey+".changeNotification")
	data, _ := notif["data"].(map[string]any)
	if data["kind"] != "moved" {
		t.Fatalf("changeNotification kind = %v, want moved (second reply must win)", data["kind"])
	}
	if data["changeRef"] != "2026-10-01T15:00:00Z" {
		t.Fatalf("changeNotification changeRef = %v, want 2026-10-01T15:00:00Z (second reply must win)", data["changeRef"])
	}
}

// TestRecordBookingChangeNotification_RejectsUnknownKind proves the kind
// enum: a token that is neither promoted, moved, nor calledOff is refused
// InvalidArgument, and writes no marker.
func TestRecordBookingChangeNotification_RejectsUnknownKind(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "changenotifybadkind")

	bookingKey := "vtx.booking.BBCNBADAJHKMNPQRSTU1"
	seedVertex(t, ctx, conn, bookingKey, "booking", nil)

	extRef := bookingKey + ":foo:2026-10-01T15:00:00Z"
	_, _, reply := cnSubmit(t, ctx, conn, cp, cons, "wdcnbadkind000001",
		`{"externalRef":"`+extRef+`","status":"completed"}`, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want an InvalidArgument rejection, got %+v", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotification"); err == nil {
		t.Fatalf("a rejected kind must write NO .changeNotification marker")
	}
}

// TestReleaseOrphanedBooking_EmitsCallOffNotice proves the call-off notice
// fires off ReleaseOrphanedBooking's own transactional outbox, in the same
// batch that tombstones the booking: exactly one external.notification,
// keyed bookingKey:calledOff:sessionKey, naming RecordBookingChangeNotification
// as its replyOp, with params.changeType=calledOff and params.status the
// drained booking's own status value.
func TestReleaseOrphanedBooking_EmitsCallOffNotice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "orphannotice")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdorphannotic0001", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdorphannotic0002", studioKey, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	bookerKey := seedIdentity(t, ctx, conn, "BBWELLCNBKRAHJKMNPQR")
	bookingKey, bookingOutcome := createBooking(t, ctx, conn, cp, cons, "wdorphannotic0003", sessionKey, bookerKey, "")
	if bookingOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bookingOutcome)
	}

	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdorphannotic0004"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("TombstoneSession", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			sessionKey, sessionKey + ".schedule",
			atStudioLnkKey(t, sessionKey, studioKey),
		}},
	}
	testutil.PublishOp(t, conn, tombstoneEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	releaseReqID := testutil.GenReqID("wdorphannotic0005")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-07-07T12:15:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:        []string{bookingKey, bookingKey + ".status", sessionKey},
			Enumerations: wdReleaseEnumerations(bookingKey),
		},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(releaseReqID))
	if err != nil {
		t.Fatalf("read outbox aspect: %v", err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	var notices []map[string]any
	for _, e := range ob.Data.Events {
		if e.EventType == "external.notification" {
			notices = append(notices, e.Payload)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("external.notification count = %d, want exactly 1 (events: %+v)", len(notices), ob.Data.Events)
	}
	notice := notices[0]
	wantKey := bookingKey + ":calledOff:" + sessionKey
	if got, _ := notice["instanceKey"].(string); got != wantKey {
		t.Fatalf("instanceKey = %q, want %q", got, wantKey)
	}
	if got, _ := notice["externalRef"].(string); got != wantKey {
		t.Fatalf("externalRef = %q, want %q", got, wantKey)
	}
	if got, _ := notice["idempotencyKey"].(string); got != wantKey {
		t.Fatalf("idempotencyKey = %q, want %q", got, wantKey)
	}
	if got, _ := notice["replyOp"].(string); got != "RecordBookingChangeNotification" {
		t.Fatalf("replyOp = %q, want RecordBookingChangeNotification", got)
	}
	params, _ := notice["params"].(map[string]any)
	if got, _ := params["changeType"].(string); got != "calledOff" {
		t.Fatalf("params.changeType = %q, want calledOff", got)
	}
	if got, _ := params["status"].(string); got != "booked" {
		t.Fatalf("params.status = %q, want booked (the drained booking's own status)", got)
	}
	if got, _ := params["bookingKey"].(string); got != bookingKey {
		t.Fatalf("params.bookingKey = %q, want %q", got, bookingKey)
	}
	if got, _ := params["sessionKey"].(string); got != sessionKey {
		t.Fatalf("params.sessionKey = %q, want %q", got, sessionKey)
	}
}
