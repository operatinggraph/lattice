package clinicdomain_test

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

// The recorded displacement of a visit by a provider's later-declared
// time-off (docs/reviews/clinic-time-off-displacement-2026-09-17.md):
//
//  1. TestClinic_TimeOffSetAtStampsEveryWrite — SetProviderTimeOff stamps
//     .timeOff.setAt = op.submittedAt and setRef = op.requestId on every
//     write, a clear (ranges=[]) included; a re-save of the same ranges gets
//     a fresh setRef.
//  2. TestClinic_BookingWritersRecordClearDisplacement — CreateAppointment and
//     RescheduleAppointment write .displacement {displaced: false, checkedFor:
//     <the provider's setRef>, at = their own submittedAt} right after their
//     time-off check passes; a provider with no .timeOff yields no checkedFor
//     (integration_test.go's TestClinic_CreateBookable pins that half).
//  3. TestClinic_EvaluateAppointmentDisplacement — the Weaver-dispatched
//     evaluation: a covered visit records {displaced: true, from, to, reason,
//     checkedFor, at = submittedAt} and emits clinic.appointmentDisplaced; a
//     re-evaluation against a re-saved time-off that still covers it carries
//     at and emits nothing; a clear flips it back (at re-stamped,
//     clinic.appointmentReinstated); a clear visit with no aspect yet records
//     {displaced: false} with no event.
//  4. TestClinic_EvaluateAppointmentDisplacementRefusals — a terminal visit is
//     the empty batch (no .displacement written); a providerKey naming another
//     provider is refused ProviderMismatch; a tombstoned appointment is
//     refused UnknownAppointment; a consumer actor is denied before the
//     script runs.

// dpEvaluateHint returns the declared posture the appointmentDisplacements
// playbook (clinic-reminders displacement.go) dispatches under: the visit root
// + .schedule and the provider's .timeOff are REQUIRED reads (the provider
// ROOT is never read — providerKey is matched against the withProvider walk);
// .status and the op's own .displacement are OPTIONAL (absent on a never-set
// status / a visit no writer has recorded a verdict for); the visit's
// withProvider walk (appointment_provider) is the one declared enumeration.
func dpEvaluateHint(apptKey, providerKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{apptKey, apptKey + ".schedule", providerKey + ".timeOff"},
		OptionalReads: []string{apptKey + ".status", apptKey + ".displacement"},
		Enumerations:  []processor.EnumerationHint{{Hub: apptKey, Relation: "withProvider", Direction: "out"}},
	}
}

// dpEvaluateEnvelope builds one EvaluateAppointmentDisplacement envelope as
// `actor` at an explicit submittedAt, with the playbook's declared posture.
// Class is LEFT EMPTY — exactly as Weaver's actuator dispatches a directOp
// (the Processor's operationType→class reverse index resolves the
// appointment vertexType handler).
func dpEvaluateEnvelope(label, actor, apptKey, providerKey, checkedFor, submittedAt string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateAppointmentDisplacement",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","providerKey":"` + providerKey + `","checkedFor":"` + checkedFor + `"}`),
		ContextHint:   dpEvaluateHint(apptKey, providerKey),
	}
}

// dpEvaluateAt submits EvaluateAppointmentDisplacement as the staff (operator)
// actor and drives it to the expected outcome.
func dpEvaluateAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, apptKey, providerKey, checkedFor, submittedAt string, want processor.MessageOutcome) {
	t.Helper()
	testutil.PublishOp(t, conn, dpEvaluateEnvelope(label, clStaffActorKey, apptKey, providerKey, checkedFor, submittedAt))
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// dpEvaluateReason submits as the staff actor and returns the script's failure
// text — the rejection REASON is what a refusal vector pins.
func dpEvaluateReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, apptKey, providerKey, checkedFor, submittedAt string) string {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, dpEvaluateEnvelope(label, clStaffActorKey, apptKey, providerKey, checkedFor, submittedAt))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %v, want Rejected", label, outcome)
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	msg := reply.Error.Message
	i := strings.Index(msg, "fail: ")
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return msg[i+len("fail: "):]
}

// dpSetTimeOffAt submits SetProviderTimeOff at an explicit submittedAt,
// asserts setAt = that submittedAt and setRef = the op's own requestId, and
// returns the setRef — the opaque per-write key checkedFor is compared
// against.
func dpSetTimeOffAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey, rangesJSON, submittedAt string) string {
	t.Helper()
	clSubmitAt(t, ctx, conn, cp, cons, label, "SetProviderTimeOff", "provider",
		`{"providerKey":"`+providerKey+`","ranges":`+rangesJSON+`}`, submittedAt, []string{providerKey}, nil, processor.OutcomeAccepted)
	od, _ := clReadDoc(t, ctx, conn, providerKey+".timeOff")["data"].(map[string]any)
	if setAt, _ := od["setAt"].(string); setAt != submittedAt {
		t.Fatalf("%s: .timeOff.setAt = %q, want the op's submittedAt %q", label, setAt, submittedAt)
	}
	setRef, _ := od["setRef"].(string)
	if setRef != testutil.GenReqID(label) {
		t.Fatalf("%s: .timeOff.setRef = %q, want the op's requestId %q", label, setRef, testutil.GenReqID(label))
	}
	return setRef
}

// dpDisplacement reads the visit's .displacement data.
func dpDisplacement(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string) map[string]any {
	t.Helper()
	doc := clReadDoc(t, ctx, conn, apptKey+".displacement")
	if doc["class"] != "appointmentDisplacement" {
		t.Fatalf("displacement class = %v, want appointmentDisplacement", doc["class"])
	}
	d, _ := doc["data"].(map[string]any)
	return d
}

// dpEvents reads the event classes the op committed to its outbox aspect
// (nil when the batch carried no events and so wrote no outbox).
func dpEvents(t *testing.T, ctx context.Context, conn *substrate.Conn, label string) []string {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(testutil.GenReqID(label)))
	if err != nil {
		return nil
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("%s: parse outbox aspect: %v", label, err)
	}
	var classes []string
	for _, e := range aspect.Data.Events {
		classes = append(classes, e.EventType)
	}
	return classes
}

const (
	dpVisitStart = "2026-07-20T09:00:00Z"
	dpVisitEnd   = "2026-07-20T09:30:00Z"
	// A range covering the visit (with a reason), a wider one covering it
	// (no reason), and one that does not touch it.
	dpCovering  = `[{"from":"2026-07-20T00:00:00Z","to":"2026-07-21T00:00:00Z","reason":"Out sick"}]`
	dpCoveringB = `[{"from":"2026-07-19T00:00:00Z","to":"2026-07-22T00:00:00Z"}]`
	dpElsewhere = `[{"from":"2026-07-06T00:00:00Z","to":"2026-07-13T00:00:00Z","reason":"Vacation"}]`
)

func TestClinic_TimeOffSetAtStampsEveryWrite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "timeoff-setat")

	providerKey := createProvider(t, ctx, conn, cp, cons, "dpprv0001", "Dr. Stamp", "Cardiology")
	ref1 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpoff0001", providerKey, dpElsewhere, "2026-01-02T00:00:00Z")
	// A re-save of the same ranges INSIDE the same wall-second is still a
	// distinct write: setAt reads the same instant, setRef does not — which
	// is what lets no in-flight evaluation close the gap on a stale verdict.
	ref2 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpoff0002", providerKey, dpElsewhere, "2026-01-02T00:00:00Z")
	if ref1 == ref2 {
		t.Fatalf("two saves must mint two setRefs; got %q twice", ref1)
	}
	// A clear stamps too — a displaced visit reads clear again off this write.
	ref3 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpoff0003", providerKey, `[]`, "2026-01-02T00:00:20Z")
	if ref3 == ref2 {
		t.Fatalf("a clear must mint its own setRef; got %q again", ref3)
	}
	od, _ := clReadDoc(t, ctx, conn, providerKey+".timeOff")["data"].(map[string]any)
	if r, _ := od["ranges"].([]any); len(r) != 0 {
		t.Fatalf("cleared ranges = %v, want []", od["ranges"])
	}
}

func TestClinic_BookingWritersRecordClearDisplacement(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "displacement-writers")

	patientKey := createPatient(t, ctx, conn, cp, cons, "dpwpat0001", "Clear Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "dpwprv0001", "Dr. Clear", "Cardiology")
	setRef := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpwoff0001", providerKey, dpElsewhere, "2026-01-02T00:00:00Z")

	// Booked outside the range at a distinct instant: the writer proves the
	// visit clear and records it, with the provider's setRef as checkedFor.
	const bookedAt = "2026-01-03T00:00:00Z"
	apptID := clSubmitAt(t, ctx, conn, cp, cons, "dpwappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+dpVisitStart+`","endsAt":"`+dpVisitEnd+`"}`,
		bookedAt, []string{patientKey, providerKey}, nil, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	d := dpDisplacement(t, ctx, conn, apptKey)
	if d["displaced"] != false || d["checkedFor"] != setRef || d["at"] != bookedAt {
		t.Fatalf("Create .displacement = %v, want {displaced: false, checkedFor: %s, at: %s}", d, setRef, bookedAt)
	}
	for _, absent := range []string{"from", "to", "reason"} {
		if _, has := d[absent]; has {
			t.Fatalf("a clear verdict carries no %s; got %v", absent, d)
		}
	}

	// The provider's time-off is re-saved (a fresh setRef), then the desk
	// moves the visit: the move records the visit clear against the NEW
	// setRef at its own instant — a displaced visit moved to a free slot reads
	// clear at once, not at the next evaluation.
	setRef2 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpwoff0002", providerKey, dpElsewhere, "2026-01-04T00:00:00Z")
	const movedAt = "2026-01-05T00:00:00Z"
	clSubmitAt(t, ctx, conn, cp, cons, "dpwmove0001", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-21T09:00:00Z","endsAt":"2026-07-21T09:30:00Z"}`,
		movedAt, clRescheduleReads(apptKey, providerKey, patientKey), clRescheduleOptionalReads(apptKey), processor.OutcomeAccepted)
	d = dpDisplacement(t, ctx, conn, apptKey)
	if d["displaced"] != false || d["checkedFor"] != setRef2 || d["at"] != movedAt {
		t.Fatalf("Reschedule .displacement = %v, want {displaced: false, checkedFor: %s, at: %s}", d, setRef2, movedAt)
	}

	// A provider with no .timeOff: the move records clear with no checkedFor.
	freeProvider := createProvider(t, ctx, conn, cp, cons, "dpwprv0002", "Dr. Free", "Cardiology")
	appt2 := "vtx.appointment." + clSubmit(t, ctx, conn, cp, cons, "dpwappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+freeProvider+`","startsAt":"2026-07-22T09:00:00Z","endsAt":"2026-07-22T09:30:00Z"}`,
		[]string{patientKey, freeProvider}, processor.OutcomeAccepted)
	clSubmitAt(t, ctx, conn, cp, cons, "dpwmove0002", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+appt2+`","provider":"`+freeProvider+`","patient":"`+patientKey+`","startsAt":"2026-07-23T09:00:00Z","endsAt":"2026-07-23T09:30:00Z"}`,
		movedAt, clRescheduleReads(appt2, freeProvider, patientKey), clRescheduleOptionalReads(appt2), processor.OutcomeAccepted)
	d = dpDisplacement(t, ctx, conn, appt2)
	if d["displaced"] != false || d["at"] != movedAt {
		t.Fatalf("Reschedule (no .timeOff) .displacement = %v, want {displaced: false, at: %s}", d, movedAt)
	}
	if _, has := d["checkedFor"]; has {
		t.Fatalf("a provider with no .timeOff yields no checkedFor; got %v", d)
	}
}

func TestClinic_EvaluateAppointmentDisplacement(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "displacement-evaluate")

	patientKey := createPatient(t, ctx, conn, cp, cons, "dpepat0001", "Displaced Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "dpeprv0001", "Dr. Away", "Cardiology")
	apptKey := "vtx.appointment." + clSubmit(t, ctx, conn, cp, cons, "dpeappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+dpVisitStart+`","endsAt":"`+dpVisitEnd+`"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)

	// Time off declared AFTER the booking, covering it. Every instant below
	// is distinct, so a fresh stamp and a carried one are told apart.
	setRef1 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpeoff0001", providerKey, dpCovering, "2026-01-02T00:00:00Z")
	const evalAt1 = "2026-01-02T00:00:05Z"
	dpEvaluateAt(t, ctx, conn, cp, cons, "dpeeval0001", apptKey, providerKey, setRef1, evalAt1, processor.OutcomeAccepted)
	d := dpDisplacement(t, ctx, conn, apptKey)
	if d["displaced"] != true || d["checkedFor"] != setRef1 || d["at"] != evalAt1 {
		t.Fatalf("covered: .displacement = %v, want {displaced: true, checkedFor: %s, at: %s}", d, setRef1, evalAt1)
	}
	if d["from"] != "2026-07-20T00:00:00Z" || d["to"] != "2026-07-21T00:00:00Z" || d["reason"] != "Out sick" {
		t.Fatalf("covered: from/to/reason = %v/%v/%v, want the covering range", d["from"], d["to"], d["reason"])
	}
	if ev := dpEvents(t, ctx, conn, "dpeeval0001"); len(ev) != 1 || ev[0] != "clinic.appointmentDisplaced" {
		t.Fatalf("covered (a flip): events = %v, want exactly clinic.appointmentDisplaced", ev)
	}

	// The provider re-saves a time-off that STILL covers the visit (a wider
	// range, no reason): checkedFor follows the new setRef, displaced stays
	// true, at is CARRIED (the notice keyed on it is not re-sent), the range
	// fields follow the live range, and no event is emitted.
	setRef2 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpeoff0002", providerKey, dpCoveringB, "2026-01-03T00:00:00Z")
	dpEvaluateAt(t, ctx, conn, cp, cons, "dpeeval0002", apptKey, providerKey, setRef2, "2026-01-03T00:00:05Z", processor.OutcomeAccepted)
	d = dpDisplacement(t, ctx, conn, apptKey)
	if d["displaced"] != true || d["checkedFor"] != setRef2 || d["at"] != evalAt1 {
		t.Fatalf("still covered: .displacement = %v, want {displaced: true, checkedFor: %s, at CARRIED %s}", d, setRef2, evalAt1)
	}
	if d["from"] != "2026-07-19T00:00:00Z" || d["to"] != "2026-07-22T00:00:00Z" {
		t.Fatalf("still covered: from/to = %v/%v, want the live range", d["from"], d["to"])
	}
	if _, has := d["reason"]; has {
		t.Fatalf("the live range carries no reason; got %v", d)
	}
	if ev := dpEvents(t, ctx, conn, "dpeeval0002"); len(ev) != 0 {
		t.Fatalf("still covered (no flip): events = %v, want none", ev)
	}

	// The provider clears their time-off: the visit is reinstated — at
	// re-stamped, the range dropped, clinic.appointmentReinstated emitted.
	setRef3 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpeoff0003", providerKey, `[]`, "2026-01-04T00:00:00Z")
	const evalAt3 = "2026-01-04T00:00:05Z"
	dpEvaluateAt(t, ctx, conn, cp, cons, "dpeeval0003", apptKey, providerKey, setRef3, evalAt3, processor.OutcomeAccepted)
	d = dpDisplacement(t, ctx, conn, apptKey)
	if d["displaced"] != false || d["checkedFor"] != setRef3 || d["at"] != evalAt3 {
		t.Fatalf("reinstated: .displacement = %v, want {displaced: false, checkedFor: %s, at: %s}", d, setRef3, evalAt3)
	}
	for _, absent := range []string{"from", "to", "reason"} {
		if _, has := d[absent]; has {
			t.Fatalf("reinstated: a clear verdict carries no %s; got %v", absent, d)
		}
	}
	if ev := dpEvents(t, ctx, conn, "dpeeval0003"); len(ev) != 1 || ev[0] != "clinic.appointmentReinstated" {
		t.Fatalf("reinstated (a flip): events = %v, want exactly clinic.appointmentReinstated", ev)
	}

	// A visit with NO .displacement yet (seeded around CreateAppointment's own
	// write) evaluated against a time-off that does not cover it: the create
	// path records {displaced: false, checkedFor, at} and emits nothing —
	// false → false is not a flip.
	bare := "vtx.appointment.DPbareApptHJKMNPQRST"
	clSeedVertex(t, ctx, conn, bare, "appointment", false)
	clSeedAspect(t, ctx, conn, bare, "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-08T15:00:00Z", "endsAt": "2026-07-08T15:30:00Z"})
	clSeedLink(t, ctx, conn, "lnk.appointment.DPbareApptHJKMNPQRST.withProvider.provider."+providerKey[len("vtx.provider."):], bare, providerKey, "withProvider", "withProvider")
	setRef4 := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dpeoff0004", providerKey, dpCovering, "2026-01-05T00:00:00Z")
	const evalAt4 = "2026-01-05T00:00:05Z"
	dpEvaluateAt(t, ctx, conn, cp, cons, "dpeeval0004", bare, providerKey, setRef4, evalAt4, processor.OutcomeAccepted)
	d = dpDisplacement(t, ctx, conn, bare)
	if d["displaced"] != false || d["checkedFor"] != setRef4 || d["at"] != evalAt4 {
		t.Fatalf("first clear verdict: .displacement = %v, want {displaced: false, checkedFor: %s, at: %s}", d, setRef4, evalAt4)
	}
	if ev := dpEvents(t, ctx, conn, "dpeeval0004"); len(ev) != 0 {
		t.Fatalf("first clear verdict (no flip): events = %v, want none", ev)
	}
}

func TestClinic_EvaluateAppointmentDisplacementRefusals(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "displacement-refusals")

	patientKey := createPatient(t, ctx, conn, cp, cons, "dprpat0001", "Refused Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "dprprv0001", "Dr. Refuse", "Cardiology")
	otherProvider := createProvider(t, ctx, conn, cp, cons, "dprprv0002", "Dr. Other", "Cardiology")
	apptKey := "vtx.appointment." + clSubmit(t, ctx, conn, cp, cons, "dprappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+dpVisitStart+`","endsAt":"`+dpVisitEnd+`"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	setRef := dpSetTimeOffAt(t, ctx, conn, cp, cons, "dproff0001", providerKey, dpCovering, "2026-01-02T00:00:00Z")

	// A providerKey naming another (live) provider: refused ProviderMismatch —
	// the visit's own withProvider link decides, never the row.
	if reason := dpEvaluateReason(t, ctx, conn, cp, cons, "dpreval0001", apptKey, otherProvider, setRef, "2026-01-02T00:00:05Z"); !strings.Contains(reason, "ProviderMismatch") {
		t.Fatalf("other provider: reason = %q, want ProviderMismatch", reason)
	}
	// The booking writer's own clear verdict is untouched by the refusal.
	if d := dpDisplacement(t, ctx, conn, apptKey); d["displaced"] != false {
		t.Fatalf("a refused evaluation must write nothing; got %v", d)
	}

	// A consumer actor holds no grant for this op: denied at step 3, before
	// the script runs — the message names no script refusal.
	{
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
			dpEvaluateEnvelope("dpreval0002", clConsumerKey, apptKey, providerKey, setRef, "2026-01-02T00:00:05Z"))
		if outcome != processor.OutcomeRejected {
			t.Fatalf("consumer actor: outcome = %v, want Rejected", outcome)
		}
		if reply.Error == nil || strings.Contains(reply.Error.Message, "fail: ") {
			t.Fatalf("consumer actor: want a step-3 authorization denial, got %+v", reply.Error)
		}
	}

	// A terminal visit is the empty batch: accepted, nothing written — the
	// clear verdict the booking writer recorded stays as it was, with its
	// original at.
	{
		before := dpDisplacement(t, ctx, conn, apptKey)
		r, o := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitAt(t, ctx, conn, cp, cons, "dprcancel001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			"2026-01-02T00:00:06Z", r, o, processor.OutcomeAccepted)
		dpEvaluateAt(t, ctx, conn, cp, cons, "dpreval0003", apptKey, providerKey, setRef, "2026-01-02T00:00:07Z", processor.OutcomeAccepted)
		after := dpDisplacement(t, ctx, conn, apptKey)
		if after["displaced"] != false || after["at"] != before["at"] || after["checkedFor"] != before["checkedFor"] {
			t.Fatalf("terminal visit: .displacement changed from %v to %v; want the empty batch", before, after)
		}
		if ev := dpEvents(t, ctx, conn, "dpreval0003"); len(ev) != 0 {
			t.Fatalf("terminal visit: events = %v, want none", ev)
		}
	}

	// A tombstoned appointment (present, isDeleted — so hydration succeeds and
	// the liveness guard, not a HydrationMiss, fires) is refused
	// UnknownAppointment and gains no .displacement.
	{
		dead := "vtx.appointment.DPdeadApptHJKMNPQRST"
		clSeedVertex(t, ctx, conn, dead, "appointment", true)
		clSeedAspect(t, ctx, conn, dead, "schedule", "appointmentSchedule", map[string]any{"startsAt": dpVisitStart, "endsAt": dpVisitEnd})
		reason := dpEvaluateReason(t, ctx, conn, cp, cons, "dpreval0004", dead, providerKey, setRef, "2026-01-02T00:00:08Z")
		if !strings.Contains(reason, "UnknownAppointment") {
			t.Fatalf("tombstoned: reason = %q, want UnknownAppointment", reason)
		}
		if !clMissing(t, ctx, conn, dead+".displacement") {
			t.Fatalf("a refused evaluation must write no .displacement on a tombstoned appointment")
		}
	}
}
