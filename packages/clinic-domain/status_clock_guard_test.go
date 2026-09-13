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
