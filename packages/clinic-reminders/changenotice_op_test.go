// RecordAppointmentChangeNotice + RecordAppointmentChangeNotification
// integration tests for the clinic-reminders Capability Package — the two
// ops' write paths driven end to end through a real Processor, on the
// harness integration_test.go builds (setupRemEnv, crReadDoc, crSubmit,
// crSubmitReply).
//
// The appointment root and its .status / .schedule aspects are SEEDED
// directly rather than minted through clinic-domain's CreateAppointment /
// SetAppointmentStatus / RescheduleAppointment: the notice op reads only the
// root (vertex_alive), .status (value / at / by), .schedule (startsAt /
// endsAt / movedAt / movedBy) and its own .changeNotice marker, so the
// fixtures carry exactly those, in the shape clinic-domain's stamped writers
// leave (packages/clinic-domain/ddls.go's stamp_status / the reschedule
// sched). The convergence PREDICATE (when each gap opens/closes) is proven on
// the real engine in changenotice_cypher_test.go.
package clinicreminders_test

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
)

const (
	cnStartsAt = "2026-07-01T15:00:00Z"
	cnEndsAt   = "2026-07-01T15:30:00Z"
	cnCancelAt = "2025-12-20T14:02:11Z"
	cnMovedAt  = "2025-12-21T09:30:00Z"
)

// cnSeedAppointment seeds an appointment root (live or tombstoned) with a
// .status and a .schedule aspect carrying exactly the fields given.
func cnSeedAppointment(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string, alive bool, status, schedule map[string]any) {
	t.Helper()
	put := func(key string, doc map[string]any) {
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	put(apptKey, map[string]any{"class": "appointment", "isDeleted": !alive, "data": map[string]any{}})
	put(apptKey+".status", map[string]any{"class": "appointmentStatus", "vertexKey": apptKey, "localName": "status", "isDeleted": false, "data": status})
	put(apptKey+".schedule", map[string]any{"class": "appointmentSchedule", "vertexKey": apptKey, "localName": "schedule", "isDeleted": false, "data": schedule})
}

// cnStaffCancelled is the .status a desk cancel leaves (SetAppointmentStatus
// under the operator grant: by staff, at = that op's submittedAt).
func cnStaffCancelled() map[string]any {
	return map[string]any{"value": "cancelled", "at": cnCancelAt, "by": "staff"}
}

// cnScheduled is a non-terminal .status with its own stamp.
func cnScheduled() map[string]any {
	return map[string]any{"value": "scheduled", "at": "2025-12-01T10:00:00Z", "by": "staff"}
}

// cnSchedule is the .schedule RescheduleAppointment leaves, with an optional
// movedAt/movedBy pair.
func cnSchedule(movedAt, movedBy string) map[string]any {
	d := map[string]any{"startsAt": cnStartsAt, "endsAt": cnEndsAt, "remindAt": "2026-06-30T15:00:00Z"}
	if movedAt != "" {
		d["movedAt"] = movedAt
		d["movedBy"] = movedBy
	}
	return d
}

// cnSeedChangeNotice writes a pre-existing .changeNotice marker, the state a
// second notice of the other kind must carry forward.
func cnSeedChangeNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string, data map[string]any) {
	t.Helper()
	key := apptKey + ".changeNotice"
	doc := map[string]any{"class": "appointmentChangeNotice", "vertexKey": apptKey, "localName": "changeNotice", "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed change notice %s: %v", key, err)
	}
}

// cnSubmit drives one RecordAppointmentChangeNotice as `actor` with the exact
// declared-read posture the appointmentChangeNotices target dispatches under
// (Reads: root, .status, .schedule; OptionalReads: .changeNotice). Class is
// LEFT EMPTY so the Processor's operationType→class reverse index is what
// resolves the handler (the target's Class pin names the same
// appointmentChangeNoticeOp DDL; the unpinned path is the stricter one to
// prove, since it is the one an ambiguous operationType would break).
func cnSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	actor, label, apptKey, kind, changeRef string, want processor.MessageOutcome) (*processor.OperationEnvelope, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordAppointmentChangeNotice",
		Actor:         actor,
		SubmittedAt:   crSubmittedAnchor,
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","kind":"` + kind + `","changeRef":"` + changeRef + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{apptKey, apptKey + ".status", apptKey + ".schedule"},
			OptionalReads: []string{apptKey + ".changeNotice"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return env, reply
}

// cnOutboxEvents reads the op's own transactional outbox and returns every
// external.notification payload it emitted plus the full event-class list,
// so a vector can assert exactly one notice went out beside the domain event.
func cnOutboxEvents(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) ([]map[string]any, []string) {
	t.Helper()
	outboxKey := processor.OutboxAspectKey(requestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
	}
	var notifs []map[string]any
	var seen []string
	for _, e := range ob.Data.Events {
		seen = append(seen, e.EventType)
		if e.EventType == "external.notification" {
			notifs = append(notifs, e.Payload)
		}
	}
	return notifs, seen
}

// cnRequireNotice asserts the outbox carries exactly one external.notification
// keyed <apptKey>:<kind>:<changeRef> beside clinic.appointmentChangeNoticeSent,
// addressed to the notification adapter with this package's replyOp and the
// visit-identifying params.
func cnRequireNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, apptKey, kind, changeRef string) {
	t.Helper()
	notifs, seen := cnOutboxEvents(t, ctx, conn, requestID)
	if len(notifs) != 1 {
		t.Fatalf("want exactly one external.notification, got %d (events: %v)", len(notifs), seen)
	}
	var sentEvent bool
	for _, e := range seen {
		if e == "clinic.appointmentChangeNoticeSent" {
			sentEvent = true
		}
	}
	if !sentEvent {
		t.Fatalf("clinic.appointmentChangeNoticeSent not emitted (events: %v)", seen)
	}
	n := notifs[0]
	wantRef := apptKey + ":" + kind + ":" + changeRef
	for _, field := range []string{"instanceKey", "externalRef", "idempotencyKey"} {
		if got, _ := n[field].(string); got != wantRef {
			t.Fatalf("external.notification %s = %q, want %q", field, got, wantRef)
		}
	}
	if got, _ := n["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := n["replyOp"].(string); got != "RecordAppointmentChangeNotification" {
		t.Fatalf("replyOp = %q, want RecordAppointmentChangeNotification", got)
	}
	params, _ := n["params"].(map[string]any)
	for field, want := range map[string]string{
		"appointmentKey": apptKey, "changeType": kind, "changeRef": changeRef,
		"startsAt": cnStartsAt, "endsAt": cnEndsAt,
	} {
		if got, _ := params[field].(string); got != want {
			t.Fatalf("params.%s = %q, want %q (params: %v)", field, got, want, params)
		}
	}
}

// TestRecordAppointmentChangeNotice_CancelledWritesMarkerAndEmits is the
// ADMIT path for a desk cancel: submitted as Weaver's dispatch actor with
// changeRef = the live .status.at, the op writes .changeNotice =
// {cancelledFor, sentAt} (no movedFor — nothing to carry) and emits exactly
// one external.notification keyed <apptKey>:cancelled:<at>.
func TestRecordAppointmentChangeNotice_CancelledWritesMarkerAndEmits(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cncancel", Instance: "cr-cncancel"})

	apptKey := "vtx.appointment.CRcnCancHJKMNPQRSTUV"
	cnSeedAppointment(t, ctx, conn, apptKey, true, cnStaffCancelled(), cnSchedule("", ""))

	env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "crcncancel01", apptKey, "cancelled", cnCancelAt, processor.OutcomeAccepted)
	if reply.PrimaryKey != apptKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, apptKey)
	}

	marker := crReadDoc(t, ctx, conn, apptKey+".changeNotice")
	if cls, _ := marker["class"].(string); cls != "appointmentChangeNotice" {
		t.Fatalf("changeNotice class = %q, want appointmentChangeNotice", cls)
	}
	md, _ := marker["data"].(map[string]any)
	if got, _ := md["cancelledFor"].(string); got != cnCancelAt {
		t.Fatalf("changeNotice cancelledFor = %q, want %q", got, cnCancelAt)
	}
	if got, _ := md["sentAt"].(string); got != crSubmittedAnchor {
		t.Fatalf("changeNotice sentAt = %q, want the op's submittedAt %q", got, crSubmittedAnchor)
	}
	if _, has := md["movedFor"]; has {
		t.Fatalf("a first cancel notice must carry no movedFor; got %v", md)
	}
	cnRequireNotice(t, ctx, conn, env.RequestID, apptKey, "cancelled", cnCancelAt)
}

// TestRecordAppointmentChangeNotice_MovedWritesMarkerAndEmits is the ADMIT
// path for a desk move on a scheduled visit: changeRef = the live
// .schedule.movedAt, the op writes movedFor and emits the notice keyed
// <apptKey>:moved:<movedAt>.
func TestRecordAppointmentChangeNotice_MovedWritesMarkerAndEmits(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnmove", Instance: "cr-cnmove"})

	apptKey := "vtx.appointment.CRcnMoveHJKMNPQRSTUV"
	cnSeedAppointment(t, ctx, conn, apptKey, true, cnScheduled(), cnSchedule(cnMovedAt, "staff"))

	env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "crcnmove0001", apptKey, "moved", cnMovedAt, processor.OutcomeAccepted)
	if reply.PrimaryKey != apptKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, apptKey)
	}
	md, _ := crReadDoc(t, ctx, conn, apptKey+".changeNotice")["data"].(map[string]any)
	if got, _ := md["movedFor"].(string); got != cnMovedAt {
		t.Fatalf("changeNotice movedFor = %q, want %q", got, cnMovedAt)
	}
	if _, has := md["cancelledFor"]; has {
		t.Fatalf("a first move notice must carry no cancelledFor; got %v", md)
	}
	cnRequireNotice(t, ctx, conn, env.RequestID, apptKey, "moved", cnMovedAt)
}

// TestRecordAppointmentChangeNotice_CancelAfterMoveCarriesMovedFor — a move
// notice already recorded movedFor; the desk then cancelled the visit. The
// cancel notice must set cancelledFor AND carry movedFor forward, or a
// correction back to scheduled would reopen the move gap and tell the
// patient about the old move again. The write lands as a bare update on the
// seeded marker (revision advances from the seed's), conditioned by the
// Processor on the revision the declared optionalRead observed
// (TestRecordAppointmentChangeNotice_MarkerUpdateIsBare pins the shape).
func TestRecordAppointmentChangeNotice_CancelAfterMoveCarriesMovedFor(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cncarry", Instance: "cr-cncarry"})

	apptKey := "vtx.appointment.CRcnCarryHJKMNPQRSTU"
	cnSeedAppointment(t, ctx, conn, apptKey, true, cnStaffCancelled(), cnSchedule(cnMovedAt, "staff"))
	cnSeedChangeNotice(t, ctx, conn, apptKey, map[string]any{"movedFor": cnMovedAt, "sentAt": "2025-12-21T09:30:05Z"})
	seeded, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, apptKey+".changeNotice")
	if err != nil {
		t.Fatalf("read seeded marker: %v", err)
	}

	env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "crcncarry001", apptKey, "cancelled", cnCancelAt, processor.OutcomeAccepted)

	md, _ := crReadDoc(t, ctx, conn, apptKey+".changeNotice")["data"].(map[string]any)
	if got, _ := md["cancelledFor"].(string); got != cnCancelAt {
		t.Fatalf("changeNotice cancelledFor = %q, want %q", got, cnCancelAt)
	}
	if got, _ := md["movedFor"].(string); got != cnMovedAt {
		t.Fatalf("changeNotice movedFor = %q, want the seeded %q carried forward (a cancel notice must not erase the move notice)", got, cnMovedAt)
	}
	if got, _ := md["sentAt"].(string); got != crSubmittedAnchor {
		t.Fatalf("changeNotice sentAt = %q, want the cancel notice's own %q", got, crSubmittedAnchor)
	}
	if rev := reply.Revisions[apptKey+".changeNotice"]; rev <= seeded.Revision {
		t.Fatalf("changeNotice revision = %d, want > the seeded %d (an update over the existing marker, not a fresh create)", rev, seeded.Revision)
	}
	cnRequireNotice(t, ctx, conn, env.RequestID, apptKey, "cancelled", cnCancelAt)
}

// cnRequireRefusal pins a refusal by its named reason, and that no marker and
// no outbox (no external.notification) were written for it.
func cnRequireRefusal(t *testing.T, ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope, reply *processor.OperationReply, apptKey, wantReason string) {
	t.Helper()
	if reply.Error == nil || !strings.Contains(reply.Error.Message, wantReason) {
		t.Fatalf("want a %s rejection, got %+v", wantReason, reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, apptKey+".changeNotice"); err == nil {
		t.Fatalf("a refused notice must write NO .changeNotice marker")
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(env.RequestID)); err == nil {
		t.Fatalf("a refused notice must leave NO outbox aspect (no external.notification)")
	}
}

// TestRecordAppointmentChangeNotice_StaleChangeRefused — the row Weaver
// dispatched from names a change the live aspect no longer carries, or one
// the desk did not make. Each is refused StaleChange, writes no marker and
// sends nothing: a stale row is refused, not trusted.
func TestRecordAppointmentChangeNotice_StaleChangeRefused(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnstale", Instance: "cr-cnstale"})

	for i, tc := range []struct {
		name, kind, changeRef string
		status, schedule      map[string]any
	}{
		{"cancelled: at differs from changeRef", "cancelled", "2025-12-20T14:00:00Z", cnStaffCancelled(), cnSchedule("", "")},
		{"cancelled: by patient", "cancelled", cnCancelAt, map[string]any{"value": "cancelled", "at": cnCancelAt, "by": "patient"}, cnSchedule("", "")},
		{"cancelled: status is not cancelled", "cancelled", cnCancelAt, cnScheduled(), cnSchedule("", "")},
		{"cancelled: legacy status with no at", "cancelled", cnCancelAt, map[string]any{"value": "cancelled"}, cnSchedule("", "")},
		{"moved: movedAt differs from changeRef", "moved", "2025-12-21T09:00:00Z", cnScheduled(), cnSchedule(cnMovedAt, "staff")},
		{"moved: movedBy patient", "moved", cnMovedAt, cnScheduled(), cnSchedule(cnMovedAt, "patient")},
		{"moved: never moved", "moved", cnMovedAt, cnScheduled(), cnSchedule("", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := string(rune('A' + i))
			apptKey := "vtx.appointment.CRcnSt" + suffix + "HJKMNPQRSTUVW"
			cnSeedAppointment(t, ctx, conn, apptKey, true, tc.status, tc.schedule)
			env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "crcnstale"+suffix, apptKey, tc.kind, tc.changeRef, processor.OutcomeRejected)
			cnRequireRefusal(t, ctx, conn, env, reply, apptKey, "StaleChange")
		})
	}
}

// TestRecordAppointmentChangeNotice_MovedTerminalRefused — a desk move
// (movedAt = changeRef, movedBy staff) on a visit that has since been
// cancelled: the move re-check passes but the visit is terminal, so the op
// refuses InvalidState — a moved-then-cancelled visit gets the cancel notice
// only. Every terminal value.
func TestRecordAppointmentChangeNotice_MovedTerminalRefused(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnterm", Instance: "cr-cnterm"})

	for i, status := range []string{"cancelled", "completed", "noShow"} {
		t.Run(status, func(t *testing.T) {
			apptKey := "vtx.appointment.CRcnTerm" + string(rune('A'+i)) + "HJKMNPQRSTU"
			cnSeedAppointment(t, ctx, conn, apptKey, true, map[string]any{"value": status, "at": cnCancelAt, "by": "staff"}, cnSchedule(cnMovedAt, "staff"))
			env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "crcnterm"+status, apptKey, "moved", cnMovedAt, processor.OutcomeRejected)
			cnRequireRefusal(t, ctx, conn, env, reply, apptKey, "InvalidState")
		})
	}
}

// TestRecordAppointmentChangeNotice_NonWeaverActorRefusedFirst proves the
// GUARD ORDER the design requires: the primordial actor-guard runs before
// the payload-shape, liveness and re-check oracles, so a non-Weaver actor is
// refused on actor grounds even when it names a TOMBSTONED appointment with
// a STALE changeRef (each of which would independently refuse, if execution
// ever reached it). crStaffActorKey holds the operator role AND the identical
// Scope:"any" grant, so step 3 authorizes it; only the script's `op.actor !=
// primordialActor["weaver"]` check stops it from having the platform notify
// an arbitrary patient about an arbitrary visit.
func TestRecordAppointmentChangeNotice_NonWeaverActorRefusedFirst(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnforged", Instance: "cr-cnforged"})

	apptKey := "vtx.appointment.CRcnForgedHJKMNPQRST"
	cnSeedAppointment(t, ctx, conn, apptKey, false, cnStaffCancelled(), cnSchedule("", ""))

	env, reply := cnSubmit(t, ctx, conn, cp, cons, crStaffActorKey, "crcnforged01", apptKey, "cancelled", "2025-12-20T14:00:00Z", processor.OutcomeRejected)
	cnRequireRefusal(t, ctx, conn, env, reply, apptKey, "AuthDenied")
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	for _, later := range []string{"UnknownAppointment", "StaleChange", "InvalidArgument"} {
		if strings.Contains(reply.Error.Message, later) {
			t.Fatalf("the actor guard must fire BEFORE the %s oracle; got %q", later, reply.Error.Message)
		}
	}
}

// TestRecordAppointmentChangeNotice_TombstonedAppointmentRefused proves the
// liveness guard (vertex_alive): a TOMBSTONED appointment (present in KV,
// isDeleted=true, with its .status / .schedule still carrying the change the
// row named — so hydration succeeds and the guard, not a HydrationMiss, is
// what fires) is refused UnknownAppointment, for both kinds, and writes no
// dangling marker and sends nothing.
func TestRecordAppointmentChangeNotice_TombstonedAppointmentRefused(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cndead", Instance: "cr-cndead"})

	apptKey := "vtx.appointment.CRcnDeadHJKMNPQRSTUV"
	cnSeedAppointment(t, ctx, conn, apptKey, false, cnStaffCancelled(), cnSchedule(cnMovedAt, "staff"))

	for _, tc := range []struct{ kind, changeRef, label string }{
		{"cancelled", cnCancelAt, "crcndead0001"},
		{"moved", cnMovedAt, "crcndead0002"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, tc.label, apptKey, tc.kind, tc.changeRef, processor.OutcomeRejected)
			cnRequireRefusal(t, ctx, conn, env, reply, apptKey, "UnknownAppointment")
		})
	}
}

// cnSubmitReply drives one RecordAppointmentChangeNotification as the staff
// (operator-role) actor with NO ContextHint — exactly as the bridge submits
// a replyOp — and returns the outcome + reply.
func cnSubmitReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, externalRef, status, result string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordAppointmentChangeNotification",
		Actor:         crStaffActorKey,
		SubmittedAt:   crSubmittedAnchor,
		Payload:       json.RawMessage(`{"externalRef":"` + externalRef + `","status":"` + status + `","result":"` + result + `"}`),
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// cnRequireChangeNotification asserts the .changeNotification aspect on the
// appointment carries exactly {kind, changeRef, status, sentAt}.
func cnRequireChangeNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey, kind, changeRef, status string) {
	t.Helper()
	doc := crReadDoc(t, ctx, conn, apptKey+".changeNotification")
	if cls, _ := doc["class"].(string); cls != "appointmentChangeNotification" {
		t.Fatalf("changeNotification class = %q, want appointmentChangeNotification", cls)
	}
	d, _ := doc["data"].(map[string]any)
	for field, want := range map[string]string{"kind": kind, "changeRef": changeRef, "status": status, "sentAt": crSubmittedAnchor} {
		if got, _ := d[field].(string); got != want {
			t.Fatalf("changeNotification %s = %q, want %q (data: %v)", field, got, want, d)
		}
	}
}

// TestRecordAppointmentChangeNotification_LandsAndOverwrites drives the
// replyOp the bridge posts after its "notification" adapter Executes for the
// event recordChangeNoticeScript emits. It submits with no Reads (the bridge
// submits none), asserts the .changeNotification aspect lands with the
// three-way split externalRef (the changeRef carries its own colons), emits
// clinic.appointmentChangeNotificationRecorded, and then proves the bare
// upsert: a second outcome for a DIFFERENT change on the same visit
// overwrites — the latest outcome wins, no once-only rejection.
func TestRecordAppointmentChangeNotification_LandsAndOverwrites(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnnotif", Instance: "cr-cnnotif"})

	apptKey := "vtx.appointment.CRcnNotifHJKMNPQRSTU"
	cnSeedAppointment(t, ctx, conn, apptKey, true, cnStaffCancelled(), cnSchedule(cnMovedAt, "staff"))

	outcome, reply := cnSubmitReply(t, ctx, conn, cp, cons, "crcnnotif001", apptKey+":moved:"+cnMovedAt, "completed", "notification sent")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("first reply: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}
	if reply.PrimaryKey != apptKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, apptKey)
	}
	cnRequireChangeNotification(t, ctx, conn, apptKey, "moved", cnMovedAt, "completed")
	_, seen := cnOutboxEvents(t, ctx, conn, reply.RequestID)
	var recorded bool
	for _, e := range seen {
		if e == "clinic.appointmentChangeNotificationRecorded" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("clinic.appointmentChangeNotificationRecorded not emitted (events: %v)", seen)
	}

	// A second outcome — the later cancel notice's reply, this one failed —
	// overwrites the marker: create-if-absent / overwrite-if-present.
	outcome, reply = cnSubmitReply(t, ctx, conn, cp, cons, "crcnnotif002", apptKey+":cancelled:"+cnCancelAt, "failed", "SMS gateway timeout")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("second reply: outcome = %v, want Accepted — the replyOp is a bare upsert, never a once-only create (reply: %+v)", outcome, reply.Error)
	}
	cnRequireChangeNotification(t, ctx, conn, apptKey, "cancelled", cnCancelAt, "failed")

	// A redelivered reply for the SAME externalRef lands too.
	outcome, reply = cnSubmitReply(t, ctx, conn, cp, cons, "crcnnotif003", apptKey+":cancelled:"+cnCancelAt, "completed", "redelivered")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("redelivered reply: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}
	cnRequireChangeNotification(t, ctx, conn, apptKey, "cancelled", cnCancelAt, "completed")
}

// TestRecordAppointmentChangeNotification_LandsOnTombstonedAppointment — the
// replyOp runs no liveness check: a visit tombstoned after its notice went
// out must still be able to record how that send ended.
func TestRecordAppointmentChangeNotification_LandsOnTombstonedAppointment(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnnotifdead", Instance: "cr-cnnotifdead"})

	apptKey := "vtx.appointment.CRcnNtfDeadHJKMNPQRS"
	cnSeedAppointment(t, ctx, conn, apptKey, false, cnStaffCancelled(), cnSchedule("", ""))

	outcome, reply := cnSubmitReply(t, ctx, conn, cp, cons, "crcnnotifdead1", apptKey+":cancelled:"+cnCancelAt, "completed", "notification sent")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("reply on a tombstoned appointment: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}
	cnRequireChangeNotification(t, ctx, conn, apptKey, "cancelled", cnCancelAt, "completed")
}

// TestRecordAppointmentChangeNotification_RejectsMalformedRef — an
// externalRef whose key is not a vtx.appointment.<NanoID>, whose kind is
// not cancelled|moved, or which has no third segment is refused
// InvalidArgument: the bridge echoes the token verbatim, so the shape is
// validated at the write.
func TestRecordAppointmentChangeNotification_RejectsMalformedRef(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "cnnotifbad", Instance: "cr-cnnotifbad"})

	for i, ref := range []string{
		"vtx.patient.CRcnNtfBadHJKMNPQRST:cancelled:" + cnCancelAt,
		"vtx.appointment.CRcnNtfBadHJKMNPQRST.status:cancelled:" + cnCancelAt,
		"vtx.appointment.CRcnNtfBadHJKMNPQRST:promoted:" + cnCancelAt,
		"vtx.appointment.CRcnNtfBadHJKMNPQRST:cancelled",
		"vtx.appointment.CRcnNtfBadHJKMNPQRST:" + cnCancelAt,
	} {
		outcome, reply := cnSubmitReply(t, ctx, conn, cp, cons, "crcnnotifbad"+string(rune('a'+i)), ref, "completed", "x")
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%q: outcome = %v, want Rejected", ref, outcome)
		}
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
			t.Fatalf("%q: want an InvalidArgument rejection, got %+v", ref, reply.Error)
		}
	}
}
