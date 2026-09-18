// RecordBookingChangeNotice's two stamp-keyed kinds — instructor and room —
// driven through the real Processor on the changenotice_op_test.go harness.
// The kinds re-check the stamp ReassignSession recorded on the session's
// .schedule (instructorChangedAt / studioChangedAt), write their own marker
// field carrying every other kind's forward, and name who leads now / where
// the class meets off the dispatching row's own params.
package wellnessreminders_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const cnStampAt = "2026-06-21T09:00:00Z"

// wrStampSchedule rewrites the seeded session's .schedule with the given
// stamps beside its startsAt — the state ReassignSession leaves.
func wrStampSchedule(t *testing.T, ctx context.Context, conn *substrate.Conn, sessionKey string, extra map[string]any) {
	t.Helper()
	data := map[string]any{"startsAt": cnStartsAt}
	for k, v := range extra {
		data[k] = v
	}
	sched := map[string]any{"class": "sessionSchedule", "vertexKey": sessionKey, "localName": "schedule", "isDeleted": false, "data": data}
	b, _ := json.Marshal(sched)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, sessionKey+".schedule", b); err != nil {
		t.Fatalf("stamp session schedule: %v", err)
	}
}

func TestRecordBookingChangeNotice_InstructorWritesMarkerAndNamesTheLeader(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcninstr", Instance: "wr-cninstr",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCNHJKMNPQRSTVW", "WRsessCNHJKMNPQRSTVW")
	wrStampSchedule(t, ctx, conn, sessionKey, map[string]any{"instructorChangedAt": cnStampAt})
	wrSeedChangeNotice(t, ctx, conn, bookingKey, map[string]any{"promotedFor": cnPromotedAt, "movedFor": cnStartsAt, "sentAt": "2025-12-20T20:41:05Z"})

	payload, _ := json.Marshal(map[string]any{"bookingKey": bookingKey, "sessionKey": sessionKey, "kind": "instructor", "changeRef": cnStampAt, "instructorName": "Sam Okafor"})
	env := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wrcninstr001"), Lane: processor.LaneDefault, OperationType: "RecordBookingChangeNotice",
		Actor: bootstrap.WeaverIdentityKey, SubmittedAt: wrSubmittedAnchor, Payload: payload,
		ContextHint: &processor.ContextHint{Reads: []string{bookingKey, bookingKey + ".status", sessionKey, sessionKey + ".schedule"}, OptionalReads: []string{bookingKey + ".changeNotice"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("instructor notice = %v (%+v), want Accepted", outcome, reply.Error)
	}
	md, _ := wrReadDoc(t, ctx, conn, bookingKey+".changeNotice")["data"].(map[string]any)
	if got, _ := md["instructorFor"].(string); got != cnStampAt {
		t.Fatalf("instructorFor = %q, want %q", got, cnStampAt)
	}
	for field, want := range map[string]string{"promotedFor": cnPromotedAt, "movedFor": cnStartsAt} {
		if got, _ := md[field].(string); got != want {
			t.Fatalf("%s = %q, want the seeded %q carried forward", field, got, want)
		}
	}
	if _, present := md["roomFor"]; present {
		t.Fatalf("roomFor must stay absent — nothing to carry")
	}
	notifs, seen := cnOutboxNotifications(t, ctx, conn, env.RequestID)
	if len(notifs) != 1 {
		t.Fatalf("want exactly one external.notification, got %d (events: %v)", len(notifs), seen)
	}
	if got, _ := notifs[0]["externalRef"].(string); got != bookingKey+":instructor:"+cnStampAt {
		t.Fatalf("externalRef = %q", got)
	}
	params, _ := notifs[0]["params"].(map[string]any)
	if got, _ := params["changeType"].(string); got != "instructor" {
		t.Fatalf("params.changeType = %q, want instructor", got)
	}
	if got, _ := params["instructorName"].(string); got != "Sam Okafor" {
		t.Fatalf("params.instructorName = %q, want Sam Okafor", got)
	}
}

func TestRecordBookingChangeNotice_RoomWritesMarkerAndClearedInstructorNamesNobody(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnroom", Instance: "wr-cnroom",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCRHJKMNPQRSTVW", "WRsessCRHJKMNPQRSTVW")
	wrStampSchedule(t, ctx, conn, sessionKey, map[string]any{"studioChangedAt": cnStampAt, "instructorChangedAt": cnStampAt})

	submit := func(label string, payload map[string]any) (*processor.OperationEnvelope, *processor.OperationReply, processor.MessageOutcome) {
		raw, _ := json.Marshal(payload)
		env := &processor.OperationEnvelope{
			RequestID: testutil.GenReqID(label), Lane: processor.LaneDefault, OperationType: "RecordBookingChangeNotice",
			Actor: bootstrap.WeaverIdentityKey, SubmittedAt: wrSubmittedAnchor, Payload: raw,
			ContextHint: &processor.ContextHint{Reads: []string{bookingKey, bookingKey + ".status", sessionKey, sessionKey + ".schedule"}, OptionalReads: []string{bookingKey + ".changeNotice"}},
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		return env, reply, outcome
	}
	env, reply, outcome := submit("wrcnroom0001", map[string]any{"bookingKey": bookingKey, "sessionKey": sessionKey, "kind": "room", "changeRef": cnStampAt, "studioName": "Riverside"})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("room notice = %v (%+v), want Accepted", outcome, reply.Error)
	}
	md, _ := wrReadDoc(t, ctx, conn, bookingKey+".changeNotice")["data"].(map[string]any)
	if got, _ := md["roomFor"].(string); got != cnStampAt {
		t.Fatalf("roomFor = %q, want %q", got, cnStampAt)
	}
	notifs, _ := cnOutboxNotifications(t, ctx, conn, env.RequestID)
	params, _ := notifs[0]["params"].(map[string]any)
	if got, _ := params["studioName"].(string); got != "Riverside" {
		t.Fatalf("params.studioName = %q, want Riverside", got)
	}

	// A cleared instructor: the row carries no instructorName; the notice
	// still goes (being un-led is the change) and names nobody.
	env, reply, outcome = submit("wrcnroom0002", map[string]any{"bookingKey": bookingKey, "sessionKey": sessionKey, "kind": "instructor", "changeRef": cnStampAt})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("un-led notice = %v (%+v), want Accepted", outcome, reply.Error)
	}
	md, _ = wrReadDoc(t, ctx, conn, bookingKey+".changeNotice")["data"].(map[string]any)
	if got, _ := md["roomFor"].(string); got != cnStampAt {
		t.Fatalf("roomFor = %q after the instructor notice, want the room notice's %q carried", got, cnStampAt)
	}
	if got, _ := md["instructorFor"].(string); got != cnStampAt {
		t.Fatalf("instructorFor = %q, want %q", got, cnStampAt)
	}
	notifs, _ = cnOutboxNotifications(t, ctx, conn, env.RequestID)
	params, _ = notifs[0]["params"].(map[string]any)
	if v, present := params["instructorName"]; present && v != nil {
		t.Fatalf("params.instructorName = %v, want null for an un-led class", v)
	}

	// StaleChange: the dispatched stamp is not the live one, and a schedule
	// with no stamp at all is a row the graph never had.
	_, reply, outcome = submit("wrcnroom0003", map[string]any{"bookingKey": bookingKey, "sessionKey": sessionKey, "kind": "room", "changeRef": "2026-06-22T09:00:00Z"})
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "StaleChange") {
		t.Fatalf("a stale room stamp = %v (%+v), want StaleChange", outcome, reply.Error)
	}
	wrStampSchedule(t, ctx, conn, sessionKey, nil)
	_, reply, outcome = submit("wrcnroom0004", map[string]any{"bookingKey": bookingKey, "sessionKey": sessionKey, "kind": "instructor", "changeRef": cnStampAt})
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "StaleChange") {
		t.Fatalf("a schedule with no stamp = %v (%+v), want StaleChange", outcome, reply.Error)
	}
}
