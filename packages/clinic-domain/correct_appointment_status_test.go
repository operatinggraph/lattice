// CorrectAppointmentStatus integration tests for the clinic-domain Capability
// Package — the explicit repair for a WRONG terminal call, i.e. the move
// SetAppointmentStatus's TerminalStatus guard refuses
// (TestClinic_TerminalStatusGuard, integration_test.go).
//
// Same external test package / harness as integration_test.go and
// set_appointment_site_test.go: seed the kernel, install rbac+identity+hygiene
// + location-domain + clinic-domain, then submit CorrectAppointmentStatus and
// assert the committed .status aspect (or its deliberate absence of change).
//
// Coverage:
//  1. TestClinic_CorrectAppointmentStatus_Corrects        — noShow → completed with a note, recording correctedFrom, holding no cells
//  2. TestClinic_CorrectAppointmentStatus_RejectsNonTerminalTarget — the op never re-opens an appointment
//  3. TestClinic_CorrectAppointmentStatus_RejectsNonTerminalCurrent — the FIRST terminal transition stays SetAppointmentStatus's job
//  4. TestClinic_CorrectAppointmentStatus_RequiresNote    — a correction rewrites a final record, so the reason is part of the write
//  5. TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace — a front-desk actor may correct only within the building it worksAt
package clinicdomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
)

// clCorrectReads returns the Reads/OptionalReads pair
// CorrectAppointmentStatus's dispatcher declares (app.js openCorrectStatus):
// the appointment is required, .status is an optionalRead — its absence is a
// legitimate state of the appointment, answered by the op's own NotTerminal
// rejection rather than a correctness error. No .schedule or
// withProvider/forPatient links: a terminal→terminal move releases no cells,
// so there is no provider/patient to validate.
func clCorrectReads(apptKey string) (reads, optionalReads []string) {
	return []string{apptKey}, []string{apptKey + ".status"}
}

// clCompleteFirstTerminal drives an appointment to its FIRST terminal status
// via SetAppointmentStatus — the precondition every correction test needs, and
// the boundary this op sits behind.
func clCompleteFirstTerminal(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, apptKey, status, providerKey, patientKey string) {
	t.Helper()
	reads, optionalReads := clStatusReads(apptKey, true, providerKey, patientKey)
	clSubmitOpt(t, ctx, conn, cp, cons, label, "SetAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"`+status+`","provider":"`+providerKey+`","patient":"`+patientKey+`"}`,
		reads, optionalReads, processor.OutcomeAccepted)
}

// submitCorrectStatusAs submits CorrectAppointmentStatus as an arbitrary actor
// on the standing path (no authContext) — mirrors submitSetAppointmentSiteAs
// (set_appointment_site_test.go), which this op's confinement guard copies.
func submitCorrectStatusAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, apptKey, status, note, actorKey string, want processor.MessageOutcome) {
	t.Helper()
	reads, optionalReads := clCorrectReads(apptKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CorrectAppointmentStatus",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload: json.RawMessage(`{"appointmentKey":"` + apptKey + `","status":"` + status +
			`","note":"` + note + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         reads,
			OptionalReads: optionalReads,
			Enumerations:  testutil.DeclaredEnumerations("CorrectAppointmentStatus", actorKey, clinicdomain.OpMetas()),
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// clStatusData reads an appointment's .status aspect data.
func clStatusData(t *testing.T, ctx context.Context, conn *substrate.Conn, apptKey string) map[string]any {
	t.Helper()
	doc := clReadDoc(t, ctx, conn, apptKey+".status")
	st, _ := doc["data"].(map[string]any)
	return st
}

// TestClinic_CorrectAppointmentStatus_Corrects is the core path the 46
// auto-marked no-shows need: an appointment wrongly left at noShow becomes
// completed, carrying the mandatory reason and the correctedFrom trace of what
// it overwrote. The released slot-claim cells stay released — a
// terminal→terminal move touches no cells, so the freed time never silently
// re-books.
func TestClinic_CorrectAppointmentStatus_Corrects(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-happy")

	patientKey := createPatient(t, ctx, conn, cp, cons, "cspat0001", "Cora Correct")
	providerKey := createProvider(t, ctx, conn, cp, cons, "csprv0001", "Dr. Correct", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "csappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-20T09:00:00Z","endsAt":"2026-07-20T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "csnoshow001", apptKey, "noShow", providerKey, patientKey)
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")

	submitCorrectStatusAs(t, ctx, conn, cp, cons, "cscorrect001", apptKey, "completed",
		"Patient was present and seen; auto no-show was wrong.", clStaffActorKey, processor.OutcomeAccepted)

	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "completed" {
		t.Fatalf("after correction, status = %v, want completed", st["value"])
	}
	if st["correctedFrom"] != "noShow" {
		t.Fatalf("correctedFrom = %v, want noShow (the value the correction overwrote)", st["correctedFrom"])
	}
	if note, _ := st["note"].(string); note != "Patient was present and seen; auto no-show was wrong." {
		t.Fatalf("note = %q, want the submitted correction reason", note)
	}
	// The op writes only .status, so the fee the wrong no-show posted is not
	// reversed here — a ledger credit is a separate, deliberate action.
	if _, present := st["noShowFeeCents"]; present {
		t.Fatalf("a correction upsert must not carry the prior noShowFeeCents forward, got %v", st["noShowFeeCents"])
	}
	// The cells stay released: nothing in this op re-claims them.
	clAssertSlotClaimReleased(t, ctx, conn, providerKey, "2026-07-20T09:00:00Z")
	clAssertSlotClaimReleased(t, ctx, conn, patientKey, "2026-07-20T09:00:00Z")
}

// TestClinic_CorrectAppointmentStatus_RejectsNonTerminalTarget pins the op's
// narrow scope: it corrects a final call, it never RE-OPENS the appointment.
// Re-opening would have to re-claim the released slot cells against whatever
// has been booked since, which this op deliberately does not do.
func TestClinic_CorrectAppointmentStatus_RejectsNonTerminalTarget(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-reopen")

	patientKey := createPatient(t, ctx, conn, cp, cons, "cspat0002", "Rita Reopen")
	providerKey := createProvider(t, ctx, conn, cp, cons, "csprv0002", "Dr. Reopen", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "csappt0002", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-21T09:00:00Z","endsAt":"2026-07-21T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "cscancel002", apptKey, "cancelled", providerKey, patientKey)

	for _, nonTerminal := range []string{"scheduled", "confirmed", "checkedIn"} {
		submitCorrectStatusAs(t, ctx, conn, cp, cons, "csreopen"+nonTerminal, apptKey, nonTerminal,
			"Booked in error, put it back.", clStaffActorKey, processor.OutcomeRejected)
	}

	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "cancelled" {
		t.Fatalf("after rejected re-open attempts, status = %v, want cancelled (unchanged)", st["value"])
	}
	if _, present := st["correctedFrom"]; present {
		t.Fatalf("a rejected correction must write nothing, got correctedFrom=%v", st["correctedFrom"])
	}
}

// TestClinic_CorrectAppointmentStatus_RejectsNonTerminalCurrent proves the
// other half of the boundary: the FIRST terminal transition stays
// SetAppointmentStatus's job (it is the transition that releases the slot
// cells). An appointment that never reached a terminal status has nothing to
// correct.
func TestClinic_CorrectAppointmentStatus_RejectsNonTerminalCurrent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-notterminal")

	patientKey := createPatient(t, ctx, conn, cp, cons, "cspat0003", "Nate Notterm")
	providerKey := createProvider(t, ctx, conn, cp, cons, "csprv0003", "Dr. Notterm", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "csappt0003", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-22T09:00:00Z","endsAt":"2026-07-22T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	// The appointment is still `scheduled` (CreateAppointment's initial value).
	submitCorrectStatusAs(t, ctx, conn, cp, cons, "csnotterm003", apptKey, "completed",
		"The visit happened.", clStaffActorKey, processor.OutcomeRejected)

	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "scheduled" {
		t.Fatalf("after a rejected correction, status = %v, want scheduled (unchanged)", st["value"])
	}
	// The cells the booking holds are untouched by the rejection — the op never
	// reaches a mutation, so nothing releases them.
	if clMissing(t, ctx, conn, clSlotClaimKey(providerKey, "2026-07-22T09:00:00Z")) {
		t.Fatalf("a rejected correction must not release the booking's held slot-claim cells")
	}
}

// TestClinic_CorrectAppointmentStatus_RequiresNote pins the one place this op
// is STRICTER than SetAppointmentStatus: a correction rewrites a record already
// treated as final, so the reason is part of the write rather than an optional
// extra.
func TestClinic_CorrectAppointmentStatus_RequiresNote(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-note")

	patientKey := createPatient(t, ctx, conn, cp, cons, "cspat0004", "Nina Note")
	providerKey := createProvider(t, ctx, conn, cp, cons, "csprv0004", "Dr. Note", "Cardiology")
	apptID := clSubmit(t, ctx, conn, cp, cons, "csappt0004", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-23T09:00:00Z","endsAt":"2026-07-23T09:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "csnoshow004", apptKey, "noShow", providerKey, patientKey)

	reads, optionalReads := clCorrectReads(apptKey)
	// Omitted entirely.
	clSubmitOpt(t, ctx, conn, cp, cons, "csnonote004", "CorrectAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"completed"}`, reads, optionalReads, processor.OutcomeRejected)
	// Present but whitespace-only — optional_string trims, so this is the same
	// "no reason given" as an omitted note and must reject identically.
	clSubmitOpt(t, ctx, conn, cp, cons, "csblanknote04", "CorrectAppointmentStatus", "appointment",
		`{"appointmentKey":"`+apptKey+`","status":"completed","note":"   "}`, reads, optionalReads, processor.OutcomeRejected)

	st := clStatusData(t, ctx, conn, apptKey)
	if st["value"] != "noShow" {
		t.Fatalf("after noteless corrections, status = %v, want noShow (unchanged)", st["value"])
	}
}

// --- workplace confinement ---------------------------------------------

const (
	casActorID  = "CLCASCNFACTRHJKMNPQR"
	casActorKey = "vtx.identity." + casActorID
	casCapKey   = "cap.identity." + casActorID
)

// casCapDoc grants the front-desk actor the SAME scope=any surface a real
// clinic-app staff session holds for CorrectAppointmentStatus ONLY — a
// dedicated capability doc rather than widening a shared one, so this file's
// guarantee stays isolated (the sasCapDoc idiom, set_appointment_site_test.go).
func casCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    casCapKey,
		Actor:                  casActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{casActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CorrectAppointmentStatus", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace is the guarantee for
// the enforcement point this op adds: one front-desk actor, one scope=any
// CorrectAppointmentStatus grant, accepted on an appointment whose provider
// practises at the building it worksAt and rejected on one that doesn't —
// mirroring TestClinic_SetAppointmentSite_ConfinedToWorkplace, proving the
// require_workplace/appointment_sites idiom this op copies actually fires here
// too, not just in the op it was copied from. Rewriting a stranger's final
// record is exactly the write the confinement exists to stop.
func TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, casCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "correct-confine")

	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "cscfbldA001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "cscfbldB001")
	providerA := createProvider(t, ctx, conn, cp, cons, "cscfprvA001", "Dr. Own Turf", "Cardiology")
	providerB := createProvider(t, ctx, conn, cp, cons, "cscfprvB001", "Dr. Elsewhere", "Cardiology")
	assignProviderSite(t, ctx, conn, cp, cons, "cscfasgA001", providerA, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "cscfasgB001", providerB, buildingB, processor.OutcomeAccepted)
	// Two patients, because clCreateAppointmentWithSite pins one fixed slot: a
	// single patient booked at both buildings would collide on PatientDoubleBook
	// before the confinement guard is ever reached.
	patientA := createPatient(t, ctx, conn, cp, cons, "cscfpatA0001", "Con Finement")
	patientB := createPatient(t, ctx, conn, cp, cons, "cscfpatB0001", "Otto Elsewhere")

	// The front-desk actor worksAt building A only — no operator holdsRole
	// link, so actor_holds_operator resolves False (cannot prove root).
	_, buildingAID, _ := substrate.ParseVertexKey(buildingA)
	clSeedLink(t, ctx, conn,
		"lnk.identity."+casActorID+".worksAt.building."+buildingAID,
		casActorKey, buildingA, "worksAt", "worksAt")

	apptAtA := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "cscfapptA01", patientA, providerA, buildingA, processor.OutcomeAccepted)
	apptAtAKey := "vtx.appointment." + apptAtA
	apptAtB := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "cscfapptB01", patientB, providerB, buildingB, processor.OutcomeAccepted)
	apptAtBKey := "vtx.appointment." + apptAtB

	// Both reach a terminal status first (the operator staff actor does this —
	// the precondition, not the thing under test).
	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "cscfnsA001", apptAtAKey, "noShow", providerA, patientA)
	clCompleteFirstTerminal(t, ctx, conn, cp, cons, "cscfnsB001", apptAtBKey, "noShow", providerB, patientB)

	submitCorrectStatusAs(t, ctx, conn, cp, cons, "cscfcorA001", apptAtAKey, "completed",
		"Seen at my own desk.", casActorKey, processor.OutcomeAccepted)
	submitCorrectStatusAs(t, ctx, conn, cp, cons, "cscfcorB001", apptAtBKey, "completed",
		"Not my building.", casActorKey, processor.OutcomeRejected)

	if got := clStatusData(t, ctx, conn, apptAtAKey)["value"]; got != "completed" {
		t.Fatalf("front-desk correction at its OWN workplace: status = %v, want completed", got)
	}
	if got := clStatusData(t, ctx, conn, apptAtBKey)["value"]; got != "noShow" {
		t.Fatalf("front-desk correction at ANOTHER building must be denied: status = %v, want noShow (unchanged) — the workplace confinement gate", got)
	}
}
