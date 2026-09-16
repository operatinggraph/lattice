// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// appointmentDDL's CreateAppointment and RescheduleAppointment — each envelope
// below declares no Reads and no OptionalReads, proving the script's own
// derive_reads(op) is what hydrates the patient/provider endpoints (and, for a
// reschedule, the appointment root, its .status/.schedule and the
// withProvider/forPatient links) rather than a caller's own declaration. Both
// ops go through actor_holds_operator's own holdsRole walk unconditionally
// (workplace-exempt short-circuit), so both declare that one Enumeration —
// Contract #2's orthogonal class (e) channel, always caller-declared and never
// something derive_reads returns.
package clinicdomain_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestCreateAppointment_UndeclaredSubmitter_BooksAppointment: a
// CreateAppointment declaring only the mandatory operator-role enumeration
// books the appointment. derive_reads' own optionalReads carries both the
// patient and provider roots, so require_live_typed sees each live endpoint
// rather than misreading an undeclared one as absent.
func TestCreateAppointment_UndeclaredSubmitter_BooksAppointment(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "createapptnodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "ndclpat0001", "Nora Declan")
	providerKey := createProvider(t, ctx, conn, cp, cons, "ndclprv0001", "Dr. Uma Vance", "Cardiology")

	reqID := testutil.GenReqID("clnodeclappt00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"patient":"` + patientKey + `","provider":"` + providerKey + `","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: clStaffActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	apptID := clNanoIDFromRequestID(reqID)
	apptKey := "vtx.appointment." + apptID
	if adoc := clReadDoc(t, ctx, conn, apptKey); adoc["class"] != "appointment" {
		t.Fatalf("appointment class = %v, want appointment — the derivation must hydrate both endpoints for the booking to run at all", adoc["class"])
	}
}

// TestRescheduleAppointment_UndeclaredSubmitter_MovesAppointment: a
// RescheduleAppointment declaring only the mandatory operator-role
// enumeration moves the appointment and rewrites .schedule. derive_reads'
// own optionalReads carries the appointment root, its .status/.schedule and
// the withProvider/forPatient links, so each read sees the live document
// rather than either tripping the read-drift guard on an undeclared live
// kv.Read or (for the appointment root) misreading an undeclared key as
// absent.
//
// The chain this proves is load-bearing, not incidental: derived ⇒ hydrated
// at step 4 ⇒ applyHydratedRevisions (commit_path.go) conditions the bare
// .schedule `update` mutation execute() emits on the revision it was
// hydrated at (Contract #3 §3.2) — for EVERY submitter, not only one that
// declares .schedule by hand. Proven by the revert method: with
// `keys.append(appt_key + ".schedule")` temporarily removed from this DDL's
// derive_reads, this exact vector fails at the read-drift guard
// (`operation "RescheduleAppointment" read vtx.appointment.<id>.schedule
// live, and nothing declared it`) rather than passing some other way — the
// derivation is what this test's PASS depends on, not a fixture detail.
func TestRescheduleAppointment_UndeclaredSubmitter_MovesAppointment(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedulenodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "ndclrspat000001", "Reed Sloane")
	providerKey := createProvider(t, ctx, conn, cp, cons, "ndclrsprv000001", "Dr. Tess Okoro", "Dermatology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "ndclrsappt0000001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clnodeclreschedule01"),
		Lane:          processor.LaneDefault,
		OperationType: "RescheduleAppointment",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","patient":"` + patientKey + `","provider":"` + providerKey + `","startsAt":"2026-07-01T16:00:00Z","endsAt":"2026-07-01T16:30:00Z"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: clStaffActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	sched := clReadDoc(t, ctx, conn, apptKey+".schedule")
	sd, _ := sched["data"].(map[string]any)
	if sd["startsAt"] != "2026-07-01T16:00:00Z" || sd["endsAt"] != "2026-07-01T16:30:00Z" {
		t.Fatalf("schedule = %v, want the moved time — the derivation must hydrate the appointment root, its .schedule and the endpoint links for the move to run at all", sd)
	}
}

// TestTombstoneAppointment_UndeclaredSubmitter_ReleasesCells: a
// TombstoneAppointment declaring only the mandatory operator-role enumeration
// tombstones a live appointment and releases its held cells. derive_reads'
// own optionalReads carries the appointment root, its .status (the terminal
// check that decides whether the release runs at all) and .schedule (the cell
// set to release) and the withProvider/forPatient links, so each read sees the
// live document rather than tripping the read-drift guard on an undeclared
// live kv.Read. The assertion is the op's effect on the freed cell: with the
// .schedule derivation removed the release reads nothing and the cell stays
// claimed.
func TestTombstoneAppointment_UndeclaredSubmitter_ReleasesCells(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "tombstonenodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "ndcltbpat000001", "Ines Farrow")
	providerKey := createProvider(t, ctx, conn, cp, cons, "ndcltbprv000001", "Dr. Kofi Mensah", "Neurology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "ndcltbappt0000001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	clAssertSlotClaimLive(t, ctx, conn, providerKey, "2026-07-01T15:00:00Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clnodecltombstone001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneAppointment",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","patient":"` + patientKey + `","provider":"` + providerKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: clStaffActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if adoc := clReadDoc(t, ctx, conn, apptKey); adoc["isDeleted"] != true {
		t.Fatalf("appointment isDeleted = %v, want true", adoc["isDeleted"])
	}
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-01T15:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-07-01T15:00:00Z")
}
