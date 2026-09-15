// RescheduleAppointment workplace-confinement — the front-desk counterpart of
// CorrectAppointmentStatus's own dedicated file (correct_appointment_status_test.go's
// casCapDoc / TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace), a
// dedicated capability doc rather than widening a shared one so this file's
// guarantee stays isolated. RescheduleAppointment grants frontOfHouse at
// scope=any (permissions.go) and resolves its confining site the SAME way
// SetAppointmentStatus/CorrectAppointmentStatus do: off the appointment's own
// withProvider -> practicesAt walk (appointment_sites), never the payload
// (ddls.go's RescheduleAppointment branch) — a front-desk actor may move only
// an appointment whose bound provider practises at a building it worksAt.
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

const (
	raActorID  = "CLRACNFACTRHJKMNPQRS"
	raActorKey = "vtx.identity." + raActorID
	raCapKey   = "cap.identity." + raActorID
)

// raCapDoc grants the front-desk actor the SAME scope=any surface a real
// clinic-app staff session holds for RescheduleAppointment ONLY — a
// dedicated capability doc rather than widening a shared one, mirroring
// casCapDoc (correct_appointment_status_test.go) / sasCapDoc
// (set_appointment_site_test.go).
func raCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    raCapKey,
		Actor:                  raActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{raActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RescheduleAppointment", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// submitRescheduleAs submits RescheduleAppointment against apptKey as an
// arbitrary actor, moving it to newStartsAt/newEndsAt, and returns the
// outcome plus the script's own failure text — clRescheduleReads/
// clRescheduleOptionalReads (integration_test.go) declare exactly what the
// real FE dispatcher does.
func submitRescheduleAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, apptKey, providerKey, patientKey, newStartsAt, newEndsAt, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"appointmentKey": apptKey, "provider": providerKey, "patient": patientKey,
		"startsAt": newStartsAt, "endsAt": newEndsAt,
	})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RescheduleAppointment",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         clRescheduleReads(apptKey, providerKey, patientKey),
			OptionalReads: clRescheduleOptionalReads(apptKey),
			Enumerations:  testutil.DeclaredEnumerations("RescheduleAppointment", actorKey, clinicdomain.OpMetas()),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		failure = reply.Error.Message
	}
	return outcome, failure
}

// TestClinic_RescheduleAppointment_ConfinedToWorkplace is the guarantee for
// RescheduleAppointment's own enforcement point, mirroring
// TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace / provider_tombstone_
// confinement_test.go's SetAppointmentStatus vector: a front-desk actor
// worksAt building A only, one scope=any RescheduleAppointment grant,
// accepted moving an appointment whose bound provider practises at building
// A, rejected for one at building B — proving the require_workplace/
// enforce_workplace_confined idiom this op shares with its siblings actually
// fires here too, not just in the op it was copied from.
func TestClinic_RescheduleAppointment_ConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, raCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "reschedule-confine")

	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "racfbldA001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "racfbldB001")
	providerA := createProvider(t, ctx, conn, cp, cons, "racfprvA001", "Dr. Own Turf", "Cardiology")
	providerB := createProvider(t, ctx, conn, cp, cons, "racfprvB001", "Dr. Elsewhere", "Cardiology")
	assignProviderSite(t, ctx, conn, cp, cons, "racfasgA001", providerA, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "racfasgB001", providerB, buildingB, processor.OutcomeAccepted)
	// Two patients: clCreateAppointmentWithSite books a single fixed slot, so
	// one patient booked at both buildings would collide on PatientDoubleBook
	// before the confinement guard is ever reached.
	patientA := createPatient(t, ctx, conn, cp, cons, "racfpatA0001", "Con Finement")
	patientB := createPatient(t, ctx, conn, cp, cons, "racfpatB0001", "Otto Elsewhere")

	// The front-desk actor worksAt building A only — no operator holdsRole
	// link, so actor_holds_operator resolves False (cannot prove root); not
	// identifiedBy either provider, so the bound-provider binder cannot
	// answer for it and only the workplace walk can.
	_, buildingAID, _ := substrate.ParseVertexKey(buildingA)
	clSeedLink(t, ctx, conn,
		"lnk.identity."+raActorID+".worksAt.building."+buildingAID,
		raActorKey, buildingA, "worksAt", "worksAt")

	apptAtA := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "racfapptA01", patientA, providerA, buildingA, processor.OutcomeAccepted)
	apptAtAKey := "vtx.appointment." + apptAtA
	apptAtB := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "racfapptB01", patientB, providerB, buildingB, processor.OutcomeAccepted)
	apptAtBKey := "vtx.appointment." + apptAtB

	got, why := submitRescheduleAs(t, ctx, conn, cp, cons, "racfmovA0001",
		apptAtAKey, providerA, patientA, "2026-07-05T10:00:00Z", "2026-07-05T10:30:00Z", raActorKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("front-desk reschedule at its OWN workplace = %v (%s), want Accepted "+
			"(the positive sibling — if this fails the negative below proves nothing)", got, why)
	}
	got, why = submitRescheduleAs(t, ctx, conn, cp, cons, "racfmovB0001",
		apptAtBKey, providerB, patientB, "2026-07-05T11:00:00Z", "2026-07-05T11:30:00Z", raActorKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("front-desk reschedule at ANOTHER building = %v (%s), want Rejected", got, why)
	}

	schedA := clReadDoc(t, ctx, conn, apptAtAKey+".schedule")
	sdA, _ := schedA["data"].(map[string]any)
	if sdA["startsAt"] != "2026-07-05T10:00:00Z" {
		t.Fatalf("front-desk reschedule at its OWN workplace: schedule.startsAt = %v, want 2026-07-05T10:00:00Z", sdA["startsAt"])
	}
	schedB := clReadDoc(t, ctx, conn, apptAtBKey+".schedule")
	sdB, _ := schedB["data"].(map[string]any)
	if sdB["startsAt"] != "2026-07-01T15:00:00Z" {
		t.Fatalf("front-desk reschedule at ANOTHER building must be denied: schedule.startsAt = %v, want unchanged 2026-07-01T15:00:00Z — the workplace confinement gate", sdB["startsAt"])
	}
}
