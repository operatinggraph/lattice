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

// The clock guards on the appointment lifecycle:
//
//  1. TestClinic_NotYetStartedGuard   — completed / noShow are refused NotYetStarted
//     while the visit is still ahead of op.submittedAt; the slot cells stay held
//     and no no-show fee is written. cancelled and checkedIn carry no clock. The
//     boundary is inclusive (integration_test.go's TestClinic_TerminalStatusGuard
//     submits exactly AT startsAt).
//  2. TestClinic_RescheduleTerminalRejected — a cancelled / completed appointment
//     is never moved (TerminalStatus): its cells were released at the terminal
//     transition, so the move would re-claim them for a visit nobody holds.
//  3. TestClinic_PastDueCheckedInNoOp — MarkPastDueNoShow no-ops with zero
//     mutations on a checkedIn appointment (the recorded arrival stays put);
//     the same op still converges a confirmed appointment to noShow normally.
//  4. TestClinic_RecordEncounterClock — RecordEncounter refuses a future visit
//     (NotYetStarted) and a cancelled / noShow one (VisitNotHeld); a completed
//     or checkedIn visit past its start is accepted and writes .documentation.
//  5. TestClinic_RescheduleResetsConfirmedAndCheckedIn — a confirmed or
//     checkedIn visit that is moved returns to scheduled (the event says
//     statusReset); a scheduled one is re-stamped unchanged; a never-set one
//     stays absent.
//  6. TestClinic_PastDueBeforeEndNoOp — a MarkPastDueNoShow whose submittedAt
//     precedes the visit's endsAt writes nothing (the schedule it hydrated was
//     moved after the lapse it was armed on); at endsAt it sweeps.

// clStaffReason submits op as the staff actor with an explicit submittedAt and
// returns the script's failure text — the rejection REASON is what these tests
// pin, since every refusal collapses to "rejected" at the outcome level.
func clStaffReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, op, payload, submittedAt string, reads, optionalReads []string) string {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         clStaffActorKey,
		SubmittedAt:   submittedAt,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads: reads, OptionalReads: optionalReads,
			Enumerations: testutil.DeclaredEnumerations(op, clStaffActorKey, clinicdomain.OpMetas()),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
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

func TestClinic_NotYetStartedGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-notyetstarted")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0030", "Early Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0030", "Dr. Early", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0030", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-15T09:00:00Z","endsAt":"2026-07-15T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)

	// One second before the visit starts: completed and noShow are both refused,
	// by name, and neither the cells nor a fee move.
	for _, status := range []string{"completed", "noShow"} {
		reason := clStaffReason(t, ctx, conn, cp, cons, "nys"+status[:4]+"0001", "SetAppointmentStatus",
			`{"appointmentKey":"`+apptKey+`","status":"`+status+`","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			"2026-07-15T08:59:59Z", reads, optionalReads)
		if !strings.HasPrefix(reason, "NotYetStarted:") {
			t.Fatalf("%s before startsAt: reason = %q, want NotYetStarted", status, reason)
		}
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-15T09:00:00Z")
	if doc := clReadDoc(t, ctx, conn, apptKey+".status"); doc != nil {
		if st, _ := doc["data"].(map[string]any); st["value"] != "scheduled" {
			t.Fatalf("status after refused early transitions = %v, want scheduled (unchanged)", st["value"])
		}
	}

	// checkedIn has no clock — arriving early is the normal case.
	clSubmitOpt(t, ctx, conn, cp, cons, "nyschk0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"checkedIn"}`, []string{apptKey}, []string{apptKey + ".status"}, processor.OutcomeAccepted)

	// From startsAt onward the same transition is accepted.
	clSubmitAt(t, ctx, conn, cp, cons, "nysdone0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"noShow","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		"2026-07-15T09:00:00Z", reads, optionalReads, processor.OutcomeAccepted)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-15T09:00:00Z")
	st, _ := clReadDoc(t, ctx, conn, apptKey+".status")["data"].(map[string]any)
	if st["value"] != "noShow" {
		t.Fatalf("status at startsAt = %v, want noShow", st["value"])
	}

	// cancelled carries no clock: a second appointment is cancelled well before it
	// starts, and its cells are released.
	appt2ID := clSubmit(t, ctx, conn, cp, cons, "mkappt0031", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-16T09:00:00Z","endsAt":"2026-07-16T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	appt2Key := "vtx.appointment." + appt2ID
	reads2, optionalReads2 := clStatusReads(appt2Key, true, providerKey, patientKey)
	clSubmitOpt(t, ctx, conn, cp, cons, "nyscanc0001", "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+appt2Key+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		reads2, optionalReads2, processor.OutcomeAccepted)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-16T09:00:00Z")

	// The side door is shut too: a cancelled future visit cannot be "corrected"
	// to completed / noShow ahead of its start (terminal→terminal reads the same
	// clock); from startsAt onward the correction lands.
	creads, coptional := clCorrectReads(appt2Key)
	for _, status := range []string{"completed", "noShow"} {
		reason := clStaffReason(t, ctx, conn, cp, cons, "nyscor"+status[:4]+"01", "CorrectAppointmentStatus",
			`{"appointmentKey":"`+appt2Key+`","status":"`+status+`","note":"seen after all"}`,
			"2026-07-16T08:59:59Z", creads, coptional)
		if !strings.HasPrefix(reason, "NotYetStarted:") {
			t.Fatalf("correct cancelled→%s before startsAt: reason = %q, want NotYetStarted", status, reason)
		}
	}
	submitCorrectStatusAt(t, ctx, conn, cp, cons, "nyscorok01", appt2Key, "completed", "seen after all", clStaffActorKey,
		"2026-07-16T09:00:00Z", processor.OutcomeAccepted)
	if st, _ := clReadDoc(t, ctx, conn, appt2Key+".status")["data"].(map[string]any); st["value"] != "completed" {
		t.Fatalf("correction at startsAt: status = %v, want completed", st["value"])
	}
}

func TestClinic_RescheduleTerminalRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resched-terminal")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0032", "Moved Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprv0032", "Dr. Moved", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "mkappt0032", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-17T09:00:00Z","endsAt":"2026-07-17T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	move := `{"appointmentKey":"` + apptKey + `","provider":"` + providerKey + `","patient":"` + patientKey + `","startsAt":"2026-07-18T10:00:00Z","endsAt":"2026-07-18T10:30:00Z"}`

	// Cancel it (cells released), then try to move it: refused TerminalStatus, and
	// the new span's cells are never claimed on either hub.
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "rtcanc0001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-17T09:00:00Z")
	reason := clStaffReason(t, ctx, conn, cp, cons, "rtmove0001", "RescheduleAppointment", move, clSubmittedAnchor,
		clRescheduleReads(apptKey, providerKey, patientKey), clRescheduleOptionalReads(apptKey))
	if !strings.HasPrefix(reason, "TerminalStatus:") {
		t.Fatalf("reschedule of a cancelled appointment: reason = %q, want TerminalStatus", reason)
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-18T10:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-07-18T10:00:00Z")
	sched, _ := clReadDoc(t, ctx, conn, apptKey+".schedule")["data"].(map[string]any)
	if sched["startsAt"] != "2026-07-17T09:00:00Z" {
		t.Fatalf("schedule after refused move = %v, want unchanged 2026-07-17T09:00:00Z", sched["startsAt"])
	}

	// A completed one is refused the same way.
	appt2ID := clSubmit(t, ctx, conn, cp, cons, "mkappt0033", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-19T09:00:00Z","endsAt":"2026-07-19T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	appt2Key := "vtx.appointment." + appt2ID
	{
		reads, optionalReads := clStatusReads(appt2Key, true, providerKey, patientKey)
		clSubmitAt(t, ctx, conn, cp, cons, "rtcompl0001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+appt2Key+`","status":"completed","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			"2026-07-19T09:30:00Z", reads, optionalReads, processor.OutcomeAccepted)
	}
	move2 := `{"appointmentKey":"` + appt2Key + `","provider":"` + providerKey + `","patient":"` + patientKey + `","startsAt":"2026-07-20T10:00:00Z","endsAt":"2026-07-20T10:30:00Z"}`
	reason = clStaffReason(t, ctx, conn, cp, cons, "rtmove0002", "RescheduleAppointment", move2, clSubmittedAnchor,
		clRescheduleReads(appt2Key, providerKey, patientKey), clRescheduleOptionalReads(appt2Key))
	if !strings.HasPrefix(reason, "TerminalStatus:") {
		t.Fatalf("reschedule of a completed appointment: reason = %q, want TerminalStatus", reason)
	}
}

// TestClinic_PastDueCheckedInNoOp proves MarkPastDueNoShow's write-path half of
// R1 (the lens's own dispatch-narrowing is pastdue_cypher_test.go's
// TestPastDue_CheckedIn): a checkedIn appointment past its endsAt no-ops with
// zero mutations rather than being swept to noShow — the recorded arrival and
// its held slot-claim cells are untouched. The positive vector — a DIFFERENT,
// still-confirmed appointment past its end — proves the exclusion is narrow:
// the same op still converges it to noShow normally.
func TestClinic_PastDueCheckedInNoOp(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-pastdue-checkedin")

	patientKey := createPatient(t, ctx, conn, cp, cons, "pdcipat01", "CheckedIn Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "pdciprv01", "Dr. Arrived", "Family")
	apptID := clSubmit(t, ctx, conn, cp, cons, "pdciappt01", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-21T09:00:00Z","endsAt":"2026-07-21T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// The desk records arrival — checkedIn carries no clock, so this lands well
	// before startsAt.
	{
		reads, optionalReads := clStatusReads(apptKey, false, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "pdcichk0001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"checkedIn"}`, reads, optionalReads, processor.OutcomeAccepted)
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-21T09:00:00Z")

	// endsAt has passed with no further staff update — the sweep dispatches and
	// must no-op, leaving the recorded arrival and its held cells alone.
	clSweepAt(t, ctx, conn, cp, cons, "pdcisweep01", apptKey, "2026-07-21T09:30:00Z")
	status := clReadDoc(t, ctx, conn, apptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "checkedIn" {
		t.Fatalf("status = %v, want checkedIn (the sweep must never overwrite a recorded arrival)", st["value"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-21T09:00:00Z")

	// Positive vector: a DIFFERENT, still-confirmed appointment past its end
	// still converges to noShow normally.
	apptID2 := clSubmit(t, ctx, conn, cp, cons, "pdciappt02", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-23T09:00:00Z","endsAt":"2026-07-23T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey2 := "vtx.appointment." + apptID2
	{
		reads, optionalReads := clStatusReads(apptKey2, false, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "pdciconf0001", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey2+`","status":"confirmed"}`, reads, optionalReads, processor.OutcomeAccepted)
	}
	clSweepAt(t, ctx, conn, cp, cons, "pdcisweep02", apptKey2, "2026-07-23T09:30:00Z")
	status2 := clReadDoc(t, ctx, conn, apptKey2+".status")
	if st2, _ := status2["data"].(map[string]any); st2["value"] != "noShow" {
		t.Fatalf("status2 = %v, want noShow (a confirmed, non-checkedIn visit still auto-no-shows normally)", st2["value"])
	}
}

// TestClinic_RecordEncounterClock proves R2: a visit that never happened
// cannot be documented, and neither can one that hasn't started yet.
// RecordEncounter is refused NotYetStarted ahead of the visit's own startsAt,
// and VisitNotHeld against a cancelled or noShow appointment — completed and
// checkedIn visits past their start are accepted and write .documentation.
func TestClinic_RecordEncounterClock(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-recordencounter-clock")

	patientKey := createPatient(t, ctx, conn, cp, cons, "reclkpat01", "Clock Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "reclkprv01", "Dr. Clock", "Family")

	// A future visit: refused NotYetStarted.
	apptID := clSubmit(t, ctx, conn, cp, cons, "reclkappt01", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-24T09:00:00Z","endsAt":"2026-07-24T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	encReads, encOptionalReads := clRecordEncounterReads(apptKey)
	reason := clStaffReason(t, ctx, conn, cp, cons, "reclknys01", "RecordEncounter",
		`{"appointmentKey":"`+apptKey+`","summary":"Too early."}`, "2026-07-24T08:59:59Z", encReads, encOptionalReads)
	if !strings.HasPrefix(reason, "NotYetStarted:") {
		t.Fatalf("RecordEncounter before startsAt: reason = %q, want NotYetStarted", reason)
	}

	// Cancel it (cancel carries no clock) — a visit that never happened cannot
	// be documented, even once its startsAt has passed.
	{
		reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "reclkcanc01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+apptKey+`","status":"cancelled","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			reads, optionalReads, processor.OutcomeAccepted)
	}
	reason = clStaffReason(t, ctx, conn, cp, cons, "reclkvnh01", "RecordEncounter",
		`{"appointmentKey":"`+apptKey+`","summary":"Never happened."}`, "2026-07-24T09:00:00Z", encReads, encOptionalReads)
	if !strings.HasPrefix(reason, "VisitNotHeld:") {
		t.Fatalf("RecordEncounter on a cancelled visit: reason = %q, want VisitNotHeld", reason)
	}

	// A DIFFERENT appointment walked to noShow: RecordEncounter is refused the
	// same way.
	apptID2 := clSubmit(t, ctx, conn, cp, cons, "reclkappt02", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-25T09:00:00Z","endsAt":"2026-07-25T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	appt2Key := "vtx.appointment." + apptID2
	encReads2, encOptionalReads2 := clRecordEncounterReads(appt2Key)
	{
		reads, optionalReads := clStatusReads(appt2Key, true, providerKey, patientKey)
		clSubmitAt(t, ctx, conn, cp, cons, "reclkns01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+appt2Key+`","status":"noShow","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			"2026-07-25T09:30:00Z", reads, optionalReads, processor.OutcomeAccepted)
	}
	reason = clStaffReason(t, ctx, conn, cp, cons, "reclkvnh02", "RecordEncounter",
		`{"appointmentKey":"`+appt2Key+`","summary":"Never happened either."}`, "2026-07-25T09:30:00Z", encReads2, encOptionalReads2)
	if !strings.HasPrefix(reason, "VisitNotHeld:") {
		t.Fatalf("RecordEncounter on a noShow visit: reason = %q, want VisitNotHeld", reason)
	}

	// Positive vector: a checkedIn visit past its start is accepted and writes
	// .documentation — the provider documenting a started visit is the
	// evidence it happened; closing the desk's record stays the desk's own job.
	apptID3 := clSubmit(t, ctx, conn, cp, cons, "reclkappt03", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-26T09:00:00Z","endsAt":"2026-07-26T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	appt3Key := "vtx.appointment." + apptID3
	{
		reads, optionalReads := clStatusReads(appt3Key, false, providerKey, patientKey)
		clSubmitOpt(t, ctx, conn, cp, cons, "reclkchk01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+appt3Key+`","status":"checkedIn"}`, reads, optionalReads, processor.OutcomeAccepted)
	}
	encReads3, encOptionalReads3 := clRecordEncounterReads(appt3Key)
	clSubmitAt(t, ctx, conn, cp, cons, "reclkdoc01", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+appt3Key+`","summary":"Seen, still checked in."}`,
		"2026-07-26T09:00:00Z", encReads3, encOptionalReads3, processor.OutcomeAccepted)
	if doc3, _ := clReadDoc(t, ctx, conn, appt3Key+".documentation")["data"].(map[string]any); doc3["documentedAt"] != "2026-07-26T09:00:00Z" {
		t.Fatalf(".documentation after RecordEncounter on a checkedIn visit = %v, want documentedAt 2026-07-26T09:00:00Z", doc3)
	}

	// Positive vector: a completed visit past its start is accepted the same
	// way.
	apptID4 := clSubmit(t, ctx, conn, cp, cons, "reclkappt04", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-27T09:00:00Z","endsAt":"2026-07-27T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	appt4Key := "vtx.appointment." + apptID4
	{
		reads, optionalReads := clStatusReads(appt4Key, true, providerKey, patientKey)
		clSubmitAt(t, ctx, conn, cp, cons, "reclkcompl01", "SetAppointmentStatus", "appointment",
			`{"appointmentKey":"`+appt4Key+`","status":"completed","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
			"2026-07-27T09:30:00Z", reads, optionalReads, processor.OutcomeAccepted)
	}
	encReads4, encOptionalReads4 := clRecordEncounterReads(appt4Key)
	clSubmitAt(t, ctx, conn, cp, cons, "reclkdoc02", "RecordEncounter", "appointment",
		`{"appointmentKey":"`+appt4Key+`","summary":"Seen and completed."}`,
		"2026-07-27T09:30:00Z", encReads4, encOptionalReads4, processor.OutcomeAccepted)
	if doc4, _ := clReadDoc(t, ctx, conn, appt4Key+".documentation")["data"].(map[string]any); doc4["documentedAt"] != "2026-07-27T09:30:00Z" {
		t.Fatalf(".documentation after RecordEncounter on a completed visit = %v, want documentedAt 2026-07-27T09:30:00Z", doc4)
	}
}

// TestClinic_RescheduleResetsConfirmedAndCheckedIn — the confirmation was for
// the old date and a recorded arrival must not exempt a moved visit from the
// past-due sweep, so RescheduleAppointment writes .status {value: scheduled}
// beside the new .schedule when the current value is confirmed or checkedIn
// (the note dropped with the transition it belonged to; the event carries
// statusReset: true). A scheduled or never-set .status is not written at all —
// its revision does not move and the event carries no statusReset. The
// terminal refusal (TestClinic_RescheduleTerminalRejected) is unchanged.
func TestClinic_RescheduleResetsConfirmedAndCheckedIn(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "resched-reset")

	patientKey := createPatient(t, ctx, conn, cp, cons, "rrpat0001", "Moved Arrival")
	providerKey := createProvider(t, ctx, conn, cp, cons, "rrprv0001", "Dr. Reset", "Cardiology")
	book := func(label, startsAt, endsAt string) string {
		apptID := clSubmit(t, ctx, conn, cp, cons, label, "CreateAppointment", "appointment",
			`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`,
			[]string{patientKey, providerKey}, processor.OutcomeAccepted)
		return "vtx.appointment." + apptID
	}
	setStatus := func(label, apptKey, status, note string) {
		reads, optionalReads := clStatusReads(apptKey, false, providerKey, patientKey)
		payload := `{"appointmentKey":"` + apptKey + `","status":"` + status + `","provider":"` + providerKey + `","patient":"` + patientKey + `"`
		if note != "" {
			payload += `,"note":"` + note + `"`
		}
		clSubmitOpt(t, ctx, conn, cp, cons, label, "SetAppointmentStatus", "appointment", payload+`}`, reads, optionalReads, processor.OutcomeAccepted)
	}
	move := func(label, apptKey, newStart, newEnd string) {
		clSubmitOpt(t, ctx, conn, cp, cons, label, "RescheduleAppointment", "appointment",
			`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"`+newStart+`","endsAt":"`+newEnd+`"}`,
			clRescheduleReads(apptKey, providerKey, patientKey), clRescheduleOptionalReads(apptKey), processor.OutcomeAccepted)
	}
	// rescheduledEvent reads the committed clinic.appointmentRescheduled
	// event off the op's own outbox aspect (the step-8 batch persists the
	// faithful EventList there; no outbox consumer runs in this harness).
	rescheduledEvent := func(label string) map[string]any {
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(testutil.GenReqID(label)))
		if err != nil {
			t.Fatalf("%s: outbox aspect: %v", label, err)
		}
		aspect, err := processor.ParseOutboxAspect(entry.Value)
		if err != nil {
			t.Fatalf("%s: parse outbox aspect: %v", label, err)
		}
		if len(aspect.Data.Events) != 1 || aspect.Data.Events[0].EventType != "clinic.appointmentRescheduled" {
			t.Fatalf("%s: events = %+v, want exactly one clinic.appointmentRescheduled", label, aspect.Data.Events)
		}
		return aspect.Data.Events[0].Payload
	}

	// checkedIn (with the desk's note) → moved → scheduled, note gone, statusReset.
	arrived := book("rrappt0001", "2026-07-20T09:00:00Z", "2026-07-20T09:30:00Z")
	setStatus("rrarr0001", arrived, "checkedIn", "arrived early")
	move("rrmove0001", arrived, "2026-07-27T09:00:00Z", "2026-07-27T09:30:00Z")
	st := clStatusData(t, ctx, conn, arrived)
	if st["value"] != "scheduled" {
		t.Fatalf("checkedIn visit after the move: status = %v, want scheduled", st["value"])
	}
	if v, present := st["note"]; present {
		t.Fatalf("the reset drops the note that belonged to the arrival, got %v", v)
	}
	if ev := rescheduledEvent("rrmove0001"); ev["statusReset"] != true {
		t.Fatalf("checkedIn move event statusReset = %v, want true (payload %v)", ev["statusReset"], ev)
	}
	// The move itself landed: old cells released, new cells held.
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-27T09:00:00Z")

	// confirmed → moved → scheduled, statusReset.
	confirmed := book("rrappt0002", "2026-07-21T09:00:00Z", "2026-07-21T09:30:00Z")
	setStatus("rrconf0001", confirmed, "confirmed", "")
	move("rrmove0002", confirmed, "2026-07-28T09:00:00Z", "2026-07-28T09:30:00Z")
	if st := clStatusData(t, ctx, conn, confirmed); st["value"] != "scheduled" {
		t.Fatalf("confirmed visit after the move: status = %v, want scheduled", st["value"])
	}
	if ev := rescheduledEvent("rrmove0002"); ev["statusReset"] != true {
		t.Fatalf("confirmed move event statusReset = %v, want true", ev["statusReset"])
	}

	// scheduled → moved: .status is re-stamped unchanged — the write is what
	// makes a concurrent self confirm hydrated on the old schedule conflict
	// under OCC — so its revision MOVES while its value does not, and the
	// event carries no statusReset (nothing was reset).
	scheduled := book("rrappt0003", "2026-07-22T09:00:00Z", "2026-07-22T09:30:00Z")
	if st := clStatusData(t, ctx, conn, scheduled); st["value"] != "scheduled" {
		t.Fatalf("precondition: CreateAppointment writes scheduled, got %v", st["value"])
	}
	rev := clRevision(t, ctx, conn, scheduled+".status")
	move("rrmove0003", scheduled, "2026-07-29T09:00:00Z", "2026-07-29T09:30:00Z")
	if got := clRevision(t, ctx, conn, scheduled+".status"); got == rev {
		t.Fatalf("moving a scheduled visit must re-stamp .status (revision stayed %d)", rev)
	}
	if st := clStatusData(t, ctx, conn, scheduled); st["value"] != "scheduled" || len(st) != 1 {
		t.Fatalf("re-stamped status = %v, want exactly {value: scheduled}", st)
	}
	if ev := rescheduledEvent("rrmove0003"); ev["statusReset"] != nil {
		t.Fatalf("scheduled move event carries statusReset = %v, want absent", ev["statusReset"])
	}

	// never-set (.status tombstoned) → moved: stays absent, no statusReset.
	unset := book("rrappt0004", "2026-07-23T09:00:00Z", "2026-07-23T09:30:00Z")
	tomb := map[string]any{"class": "appointmentStatus", "isDeleted": true,
		"vertexKey": unset, "localName": "status", "data": map[string]any{"value": "scheduled"}}
	b, _ := json.Marshal(tomb)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, unset+".status", b); err != nil {
		t.Fatalf("tombstone status: %v", err)
	}
	move("rrmove0004", unset, "2026-07-30T09:00:00Z", "2026-07-30T09:30:00Z")
	if doc := clReadDoc(t, ctx, conn, unset+".status"); doc["isDeleted"] != true {
		t.Fatalf("moving a visit with no live .status revived it: %v", doc)
	}
	if ev := rescheduledEvent("rrmove0004"); ev["statusReset"] != nil {
		t.Fatalf("never-set move event carries statusReset = %v, want absent", ev["statusReset"])
	}
}

// TestClinic_PastDueBeforeEndNoOp — the sweep's clock conjunct: a dispatch
// that raced a RescheduleAppointment hydrates the MOVED .schedule, whose
// endsAt is ahead of the dispatch's own submittedAt, so it must write nothing
// (the lapse it was armed on belonged to the old date; the lens re-arms at the
// new endsAt). The boundary is inclusive: submitted AT endsAt sweeps.
func TestClinic_PastDueBeforeEndNoOp(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "status-pastdue-clock")

	patientKey := createPatient(t, ctx, conn, cp, cons, "pdclkpat01", "Moved Patient")
	providerKey := createProvider(t, ctx, conn, cp, cons, "pdclkprv01", "Dr. Moved", "Family")
	apptID := clSubmit(t, ctx, conn, cp, cons, "pdclkappt01", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-21T09:00:00Z","endsAt":"2026-07-21T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	// The visit is moved a week out; the sweep armed on the old endsAt
	// dispatches one second before the old end passed — and reads the new.
	clSubmitOpt(t, ctx, conn, cp, cons, "pdclkmove01", "RescheduleAppointment", "appointment",
		`{"appointmentKey":"`+apptKey+`","provider":"`+providerKey+`","patient":"`+patientKey+`","startsAt":"2026-07-28T09:00:00Z","endsAt":"2026-07-28T09:30:00Z"}`,
		clRescheduleReads(apptKey, providerKey, patientKey), clRescheduleOptionalReads(apptKey), processor.OutcomeAccepted)
	rev := clRevision(t, ctx, conn, apptKey+".status")
	for i, at := range []string{"2026-07-21T09:29:59Z", "2026-07-21T09:30:00Z", "2026-07-28T09:29:59Z"} {
		clSweepAt(t, ctx, conn, cp, cons, fmt.Sprintf("pdclkswp%02d", i+1), apptKey, at)
		if got := clRevision(t, ctx, conn, apptKey+".status"); got != rev {
			t.Fatalf("sweep at %s (endsAt 2026-07-28T09:30:00Z) wrote .status (%d → %d); want no-op", at, rev, got)
		}
	}
	if st := clStatusData(t, ctx, conn, apptKey); st["value"] != "scheduled" {
		t.Fatalf("status after early sweeps = %v, want scheduled", st["value"])
	}
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-28T09:00:00Z")

	// AT the new endsAt (inclusive): the sweep lands.
	clSweepAt(t, ctx, conn, cp, cons, "pdclkswp04", apptKey, "2026-07-28T09:30:00Z")
	if st := clStatusData(t, ctx, conn, apptKey); st["value"] != "noShow" {
		t.Fatalf("status after the sweep at endsAt = %v, want noShow", st["value"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-28T09:00:00Z")
}
