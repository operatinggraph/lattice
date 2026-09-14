package clinicdomain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
)

// The patient-self visit clock and the fee it writes — the late-cancel fee is
// a fact on the .status aspect, and clinic-ledger bills the fact
// (docs/reviews/clinic-late-cancel-fee-2026-09-13.md):
//
//  1. TestClinic_SelfCancel_VisitStarted       — a patient's own cancel at or after startsAt is refused VisitStarted
//  2. TestClinic_SelfReschedule_VisitStarted   — so is a patient's own reschedule
//  3. TestClinic_SelfReschedule_LateWindow     — inside startsAt − 24h the move is refused LateReschedule; one second earlier it lands
//  4. TestClinic_SelfCancel_LateWindow_OwesFee — inside the window the cancel lands with lateCancel + the 2500 fee, and a same-value re-cancel (self or staff) carries both forward
//  5. TestClinic_SelfCancel_Open_NoFee         — before the window the cancel carries neither field
//  6. TestClinic_StaffCancel_InsideWindow_NoFee — the staff path has no window
//  7. TestClinic_CorrectAppointmentStatus_NoShowCarriesFee — a correction onto noShow writes the fee (default / caller / refused non-positive); onto completed / cancelled writes none
//
// Every submit carries an explicit submittedAt: the clock is read against the
// visit's own startsAt, and both boundaries are inclusive on the stricter side.

// lcSelfSubmit submits op as the linked consumer on the self path (authContext
// target = the caller's own identity) with an explicit submittedAt and returns
// the outcome plus, on a rejection, the script's failure text.
func lcSelfSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op, payload, submittedAt string, reads, optionalReads []string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         clConsumerKey,
		SubmittedAt:   submittedAt,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: optionalReads},
		AuthContext:   &processor.AuthContext{Target: clConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		return outcome, ""
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	msg := reply.Error.Message
	i := strings.Index(msg, "fail: ")
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return outcome, msg[i+len("fail: "):]
}

// lcSelfCancel is the patient's own SetAppointmentStatus(cancelled) with the
// dispatcher's declared reads (app.js setStatus on the terminal branch: the
// appointment, its .schedule, both endpoint links; optional: the current
// .status and the identifiedBy ownership probe).
func lcSelfCancel(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, apptKey, providerKey, patientKey, note, submittedAt string) (processor.MessageOutcome, string) {
	t.Helper()
	payload := `{"appointmentKey":"` + apptKey + `","status":"cancelled","provider":"` + providerKey + `","patient":"` + patientKey + `"`
	if note != "" {
		payload += `,"note":"` + note + `"`
	}
	payload += `}`
	patientID := patientKey[len("vtx.patient."):]
	return lcSelfSubmit(t, ctx, conn, cp, cons, label, "SetAppointmentStatus", payload, submittedAt,
		clRescheduleReads(apptKey, providerKey, patientKey),
		[]string{apptKey + ".status", "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID})
}

// lcSelfReschedule is the patient's own RescheduleAppointment to
// [newStart, newEnd) with the dispatcher's declared reads (app.js
// submitReschedule).
func lcSelfReschedule(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, apptKey, providerKey, patientKey, newStart, newEnd, submittedAt string) (processor.MessageOutcome, string) {
	t.Helper()
	payload := `{"appointmentKey":"` + apptKey + `","provider":"` + providerKey + `","patient":"` + patientKey +
		`","startsAt":"` + newStart + `","endsAt":"` + newEnd + `"}`
	patientID := patientKey[len("vtx.patient."):]
	return lcSelfSubmit(t, ctx, conn, cp, cons, label, "RescheduleAppointment", payload, submittedAt,
		clRescheduleReads(apptKey, providerKey, patientKey),
		[]string{apptKey + ".status", "lnk.patient." + patientID + ".identifiedBy.identity." + clConsumerID})
}

// lcLinkedPatient registers a patient identified by the consumer identity —
// the shape every self-path test needs — and returns its key.
func lcLinkedPatient(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, fullName string) string {
	t.Helper()
	clSeedVertex(t, ctx, conn, clConsumerKey, "identity", false)
	patientID := clSubmitOpt(t, ctx, conn, cp, cons, label, "CreatePatient", "patient",
		`{"fullName":"`+fullName+`","identityKey":"`+clConsumerKey+`"}`,
		[]string{clConsumerKey}, []string{clConsumerKey + ".patientClaim"}, processor.OutcomeAccepted)
	return "vtx.patient." + patientID
}

// lcBook books [startsAt, startsAt+30m) for the patient with the provider and
// returns the appointment key.
func lcBook(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey, providerKey, startsAt, endsAt string) string {
	t.Helper()
	apptID := clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	return "vtx.appointment." + apptID
}

// lcAssertNoFee pins that a .status carries neither late-cancel field.
func lcAssertNoFee(t *testing.T, st map[string]any, what string) {
	t.Helper()
	if v, present := st["lateCancel"]; present {
		t.Fatalf("%s: lateCancel = %v, want absent", what, v)
	}
	if v, present := st["noShowFeeCents"]; present {
		t.Fatalf("%s: noShowFeeCents = %v, want absent", what, v)
	}
}

// lcAssertLateFee pins that a .status carries the late cancel exactly as the
// script writes it: lateCancel true and noShowFeeCents equal to the script's
// own default — the value asserted against its source, not merely present.
func lcAssertLateFee(t *testing.T, st map[string]any, what string) {
	t.Helper()
	if st["value"] != "cancelled" {
		t.Fatalf("%s: value = %v, want cancelled", what, st["value"])
	}
	if st["lateCancel"] != true {
		t.Fatalf("%s: lateCancel = %v, want true", what, st["lateCancel"])
	}
	if st["noShowFeeCents"] != 2500.0 {
		t.Fatalf("%s: noShowFeeCents = %v, want 2500 (the script's default no-show fee)", what, st["noShowFeeCents"])
	}
}

func TestClinic_SelfCancel_VisitStarted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfcancel-started")

	patientKey := lcLinkedPatient(t, ctx, conn, cp, cons, "lcpat0001", "Late Starter")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0001", "Dr. Clock", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0001", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// Exactly AT startsAt (the boundary is inclusive) and well after it.
	for i, at := range []string{"2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z"} {
		outcome, reason := lcSelfCancel(t, ctx, conn, cp, cons, fmt.Sprintf("lcstart%04d", i+1), apptKey, providerKey, patientKey, "", at)
		if outcome != processor.OutcomeRejected || !strings.HasPrefix(reason, "VisitStarted:") {
			t.Fatalf("self cancel at %s: outcome = %v reason = %q, want Rejected VisitStarted", at, outcome, reason)
		}
	}
	// Nothing moved: still scheduled, cells still held.
	if st := clStatusData(t, ctx, conn, apptKey); st["value"] != "scheduled" {
		t.Fatalf("status after refused self cancels = %v, want scheduled (unchanged)", st["value"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
}

func TestClinic_SelfReschedule_VisitStarted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfresched-started")

	patientKey := lcLinkedPatient(t, ctx, conn, cp, cons, "lcpat0002", "Late Mover")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0002", "Dr. Clock", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0002", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// The new time is in the future of every submittedAt here, so the only
	// clock that can refuse is the visit's own.
	for i, at := range []string{"2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z"} {
		outcome, reason := lcSelfReschedule(t, ctx, conn, cp, cons, fmt.Sprintf("lcrsstart%03d", i+1), apptKey, providerKey, patientKey,
			"2026-07-22T10:00:00Z", "2026-07-22T10:30:00Z", at)
		if outcome != processor.OutcomeRejected || !strings.HasPrefix(reason, "VisitStarted:") {
			t.Fatalf("self reschedule at %s: outcome = %v reason = %q, want Rejected VisitStarted", at, outcome, reason)
		}
	}
	sched, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if sched["startsAt"] != "2026-07-20T09:00:00Z" {
		t.Fatalf("schedule after refused moves = %v, want unchanged 2026-07-20T09:00:00Z", sched["startsAt"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-22T10:00:00Z")
}

func TestClinic_SelfReschedule_LateWindow(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfresched-late")

	patientKey := lcLinkedPatient(t, ctx, conn, cp, cons, "lcpat0003", "Window Mover")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0003", "Dr. Window", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0003", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// Exactly at startsAt − 24h: the window is open (inclusive), the move is refused.
	outcome, reason := lcSelfReschedule(t, ctx, conn, cp, cons, "lcrslate0001", apptKey, providerKey, patientKey,
		"2026-07-22T10:00:00Z", "2026-07-22T10:30:00Z", "2026-07-19T09:00:00Z")
	if outcome != processor.OutcomeRejected || !strings.HasPrefix(reason, "LateReschedule:") {
		t.Fatalf("self reschedule at startsAt-24h: outcome = %v reason = %q, want Rejected LateReschedule", outcome, reason)
	}
	sched, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if sched["startsAt"] != "2026-07-20T09:00:00Z" {
		t.Fatalf("schedule after refused late move = %v, want unchanged", sched["startsAt"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")

	// One second before the window opens: the move lands.
	outcome, reason = lcSelfReschedule(t, ctx, conn, cp, cons, "lcrsopen0001", apptKey, providerKey, patientKey,
		"2026-07-22T10:00:00Z", "2026-07-22T10:30:00Z", "2026-07-19T08:59:59Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self reschedule at startsAt-24h-1s: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	sched, _ = clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if sched["startsAt"] != "2026-07-22T10:00:00Z" {
		t.Fatalf("schedule after open move = %v, want 2026-07-22T10:00:00Z", sched["startsAt"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-22T10:00:00Z")
}

func TestClinic_SelfCancel_LateWindow_OwesFee(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfcancel-late")

	patientKey := lcLinkedPatient(t, ctx, conn, cp, cons, "lcpat0004", "Window Canceller")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0004", "Dr. Window", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0004", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// Exactly at startsAt − 24h: the cancel lands, owing the fee, and the
	// cells are released like any first terminal transition.
	outcome, reason := lcSelfCancel(t, ctx, conn, cp, cons, "lclate00001", apptKey, providerKey, patientKey, "", "2026-07-19T09:00:00Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self cancel at startsAt-24h: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st := clStatusData(t, ctx, conn, apptKey)
	lcAssertLateFee(t, st, "late self cancel")
	if _, present := st["note"]; present {
		t.Fatalf("noteless late cancel carries a note: %v", st["note"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-07-20T09:00:00Z")

	// A same-value re-cancel by the patient (now with a note, after the visit
	// would have started — the clock applies only to the first transition)
	// carries both fee fields forward and records the note.
	outcome, reason = lcSelfCancel(t, ctx, conn, cp, cons, "lclate00002", apptKey, providerKey, patientKey, "changed my mind twice", "2026-07-20T12:00:00Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self re-cancel: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st = clStatusData(t, ctx, conn, apptKey)
	lcAssertLateFee(t, st, "self re-cancel")
	if st["note"] != "changed my mind twice" {
		t.Fatalf("self re-cancel note = %v, want the submitted note", st["note"])
	}

	// A same-value re-cancel by staff (noteless) carries both forward and
	// clears the note — the note keeps its clear-on-omit semantics.
	reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
	clSubmitAt(t, ctx, conn, cp, cons, "lclate00003", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		"2026-07-20T12:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	st = clStatusData(t, ctx, conn, apptKey)
	lcAssertLateFee(t, st, "staff re-cancel")
	if _, present := st["note"]; present {
		t.Fatalf("noteless staff re-cancel must clear the note, got %v", st["note"])
	}

	// The waiver is the correction's own fee-less write.
	submitCorrectStatusAt(t, ctx, conn, cp, cons, "lclate00004", apptKey, "cancelled", "Clinic called it off; fee waived.", clStaffActorKey,
		"2026-07-20T12:00:00Z", processor.OutcomeAccepted)
	st = clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "cancelled" || st["correctedFrom"] != "cancelled" {
		t.Fatalf("waiver correction: value = %v correctedFrom = %v, want cancelled / cancelled", st["value"], st["correctedFrom"])
	}
	lcAssertNoFee(t, st, "waiver correction")
}

func TestClinic_SelfCancel_Open_NoFee(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "selfcancel-open")

	patientKey := lcLinkedPatient(t, ctx, conn, cp, cons, "lcpat0005", "Early Canceller")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0005", "Dr. Early", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0005", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// One second before the window opens: free.
	outcome, reason := lcSelfCancel(t, ctx, conn, cp, cons, "lcopen00001", apptKey, providerKey, patientKey, "", "2026-07-19T08:59:59Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self cancel at startsAt-24h-1s: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "cancelled" {
		t.Fatalf("status = %v, want cancelled", st["value"])
	}
	lcAssertNoFee(t, st, "open self cancel")
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")

	// A same-value re-cancel of a fee-less cancel carries nothing in.
	outcome, reason = lcSelfCancel(t, ctx, conn, cp, cons, "lcopen00002", apptKey, providerKey, patientKey, "", "2026-07-20T12:00:00Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self re-cancel: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	lcAssertNoFee(t, clStatusData(t, ctx, conn, apptKey), "re-cancel of an open cancel")
}

func TestClinic_StaffCancel_InsideWindow_NoFee(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "staffcancel-window")

	patientKey := createPatient(t, ctx, conn, cp, cons, "lcpat0006", "Desk Cancelled")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0006", "Dr. Desk", "Cardiology")
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0006", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")

	// The desk cancels an hour before the visit: the clinic may be the one
	// calling it off, so no fee — and no clock either (after startsAt too).
	reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
	clSubmitAt(t, ctx, conn, cp, cons, "lcstaff0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		"2026-07-20T08:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "cancelled" {
		t.Fatalf("status = %v, want cancelled", st["value"])
	}
	lcAssertNoFee(t, st, "staff cancel inside the window")
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")

	appt2Key := lcBook(t, ctx, conn, cp, cons, "lcappt0007", patientKey, providerKey, "2026-07-21T09:00:00Z", "2026-07-21T09:30:00Z")
	reads2, optionalReads2 := clStatusReads(appt2Key, true, providerKey, patientKey)
	clSubmitAt(t, ctx, conn, cp, cons, "lcstaff0002", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+appt2Key+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		"2026-07-21T09:15:00Z", reads2, optionalReads2, processor.OutcomeAccepted)
	lcAssertNoFee(t, clStatusData(t, ctx, conn, appt2Key), "staff cancel after startsAt")
}

// lcCorrect submits CorrectAppointmentStatus as staff with a raw payload
// (so a caller-supplied noShowFeeCents can be carried) and returns the
// outcome plus the failure text on a rejection.
func lcCorrect(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, apptKey, payload, submittedAt string) (processor.MessageOutcome, string) {
	t.Helper()
	reads, optionalReads := clCorrectReads(apptKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CorrectAppointmentStatus",
		Actor:         clStaffActorKey,
		SubmittedAt:   submittedAt,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads: reads, OptionalReads: optionalReads,
			Enumerations: testutil.DeclaredEnumerations("CorrectAppointmentStatus", clStaffActorKey, clinicdomain.OpMetas()),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		return outcome, ""
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	msg := reply.Error.Message
	i := strings.Index(msg, "fail: ")
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return outcome, msg[i+len("fail: "):]
}

func TestClinic_CorrectAppointmentStatus_NoShowCarriesFee(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-fee")

	patientKey := createPatient(t, ctx, conn, cp, cons, "lcpat0008", "Corrected Absentee")
	providerKey := createProvider(t, ctx, conn, cp, cons, "lcprv0008", "Dr. Correct", "Cardiology")
	const after = "2026-07-20T12:00:00Z"

	// A desk-cancelled visit corrected to noShow: the default fee lands, and
	// correctedFrom records the cancel it overwrote.
	apptKey := lcBook(t, ctx, conn, cp, cons, "lcappt0008", patientKey, providerKey, "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")
	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "lccorr00001", apptKey, "cancelled", providerKey, patientKey)
	outcome, reason := lcCorrect(t, ctx, conn, cp, cons, "lccorr00002", apptKey,
		`{"appointmentKey":"`+apptKey+`","status":"noShow","note":"Never came; the cancel was logged in error."}`, after)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("correct cancelled→noShow: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "noShow" || st["correctedFrom"] != "cancelled" {
		t.Fatalf("value = %v correctedFrom = %v, want noShow / cancelled", st["value"], st["correctedFrom"])
	}
	if st["noShowFeeCents"] != 2500.0 {
		t.Fatalf("noShowFeeCents = %v, want 2500 (the script's default, the same SetAppointmentStatus writes)", st["noShowFeeCents"])
	}

	// A caller-supplied fee is written as given; a non-positive one is refused
	// by name and leaves the row untouched.
	outcome, reason = lcCorrect(t, ctx, conn, cp, cons, "lccorr00003", apptKey,
		`{"appointmentKey":"`+apptKey+`","status":"noShow","note":"Specialist slot; higher fee.","noShowFeeCents":4000}`, after)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("correct with fee 4000: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	if st = clStatusData(t, ctx, conn, apptKey); st["noShowFeeCents"] != 4000.0 {
		t.Fatalf("noShowFeeCents = %v, want the caller's 4000", st["noShowFeeCents"])
	}
	for i, bad := range []string{"0", "-500"} {
		outcome, reason = lcCorrect(t, ctx, conn, cp, cons, fmt.Sprintf("lccorrbad%04d", i+1), apptKey,
			`{"appointmentKey":"`+apptKey+`","status":"noShow","note":"bad fee","noShowFeeCents":`+bad+`}`, after)
		if outcome != processor.OutcomeRejected || !strings.HasPrefix(reason, "InvalidArgument: noShowFeeCents") {
			t.Fatalf("correct with fee %s: outcome = %v reason = %q, want Rejected InvalidArgument: noShowFeeCents", bad, outcome, reason)
		}
	}
	if st = clStatusData(t, ctx, conn, apptKey); st["noShowFeeCents"] != 4000.0 || st["note"] != "Specialist slot; higher fee." {
		t.Fatalf("a refused fee must write nothing: fee = %v note = %v", st["noShowFeeCents"], st["note"])
	}

	// Off noShow: completed writes no fee (the ledger reverses the posted
	// charge — its missing_reversal gap), and so does cancelled.
	outcome, reason = lcCorrect(t, ctx, conn, cp, cons, "lccorr00004", apptKey,
		`{"appointmentKey":"`+apptKey+`","status":"completed","note":"Seen after all."}`, after)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("correct noShow→completed: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st = clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "completed" || st["correctedFrom"] != "noShow" {
		t.Fatalf("value = %v correctedFrom = %v, want completed / noShow", st["value"], st["correctedFrom"])
	}
	lcAssertNoFee(t, st, "correction to completed")

	// A caller-supplied fee on a non-noShow target is ignored, never written.
	outcome, reason = lcCorrect(t, ctx, conn, cp, cons, "lccorr00005", apptKey,
		`{"appointmentKey":"`+apptKey+`","status":"cancelled","note":"Clinic closed that day.","noShowFeeCents":4000}`, after)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("correct completed→cancelled: outcome = %v reason = %q, want Accepted", outcome, reason)
	}
	st = clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "cancelled" || st["correctedFrom"] != "completed" {
		t.Fatalf("value = %v correctedFrom = %v, want cancelled / completed", st["value"], st["correctedFrom"])
	}
	lcAssertNoFee(t, st, "correction to cancelled")
}
