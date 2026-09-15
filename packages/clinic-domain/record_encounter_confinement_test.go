// RecordEncounter workplace-confinement. RecordEncounter grants only
// [operator, provider] at scope=any (permissions.go) — frontOfHouse is NOT
// among its grantees, unlike RescheduleAppointment/SetAppointmentStatus. Its
// script (ddls.go's RecordEncounter branch) still carries the SAME
// require_workplace/enforce_workplace_confined fallback those ops do — "a
// bound provider may document THEIR OWN appointment; anyone else falls back
// to the workplace walk, which a provider (carrying no worksAt anchor) never
// clears, so an unbound caller is denied rather than merely unconfined" — but
// because the ONLY non-operator grantee is `provider`, and a provider
// identity ordinarily holds no `worksAt` link at all, that fallback has no
// naturally-occurring ACCEPT case. This file proves the fallback is real
// rather than dead code the confinement-guard-tested-only-as-operator class
// would otherwise hide: a provider-role actor is granted worksAt (a shape
// this product does not mint on its own, but nothing in the script forbids —
// the guard reads worksAt links off whatever identity holds them, not off a
// provider-shaped vertex specifically), is NOT identifiedBy-bound to either
// appointment's own provider, and is accepted documenting the visit at the
// building it worksAt and refused for one elsewhere — the same accept/reject
// pair every other require_workplace-confined op's own file proves, run
// through the one grantee this op actually has.
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
	reActorID  = "CLRECNFACTRHJKMNPQRS"
	reActorKey = "vtx.identity." + reActorID
	reCapKey   = "cap.identity." + reActorID
)

// reCapDoc grants the unbound provider-role actor the SAME scope=any
// RecordEncounter surface the operator holds — the point of the confinement
// test is that the capability plane cannot distinguish an unbound provider
// from root, so if the fallback holds it holds entirely inside the script.
func reCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    reCapKey,
		Actor:                  reActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{reActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordEncounter", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "provider")},
	}
}

// submitRecordEncounterAs submits RecordEncounter against apptKey as an
// arbitrary actor and returns the outcome plus the script's own failure
// text — clRecordEncounterReads (integration_test.go) declares exactly what
// the real FE dispatcher does.
func submitRecordEncounterAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, apptKey, submittedAt, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	reads, optionalReads := clRecordEncounterReads(apptKey)
	payload, _ := json.Marshal(map[string]any{"appointmentKey": apptKey, "summary": "Seen, stable."})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordEncounter",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "appointment",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         reads,
			OptionalReads: optionalReads,
			Enumerations:  testutil.DeclaredEnumerations("RecordEncounter", actorKey, clinicdomain.OpMetas()),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		failure = reply.Error.Message
	}
	return outcome, failure
}

// TestClinic_RecordEncounter_UnboundProviderConfinedToWorkplace proves
// RecordEncounter's workplace fallback (ddls.go: "anyone else falls back to
// the workplace walk") actually runs and can accept, not only deny: an
// unbound provider-role actor granted a worksAt link documents a visit at
// the building it worksAt and is refused for one elsewhere — mirroring
// TestClinic_RescheduleAppointment_ConfinedToWorkplace /
// TestClinic_CorrectAppointmentStatus_ConfinedToWorkplace, run through the
// one grantee (`provider`) this op actually has.
func TestClinic_RecordEncounter_UnboundProviderConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, reCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "record-encounter-confine")

	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "recfbldA001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "recfbldB001")
	providerA := createProvider(t, ctx, conn, cp, cons, "recfprvA001", "Dr. Own Turf", "Cardiology")
	providerB := createProvider(t, ctx, conn, cp, cons, "recfprvB001", "Dr. Elsewhere", "Cardiology")
	assignProviderSite(t, ctx, conn, cp, cons, "recfasgA001", providerA, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "recfasgB001", providerB, buildingB, processor.OutcomeAccepted)
	patientA := createPatient(t, ctx, conn, cp, cons, "recfpatA0001", "Con Finement")
	patientB := createPatient(t, ctx, conn, cp, cons, "recfpatB0001", "Otto Elsewhere")

	// The actor holds `provider` (RecordEncounter's only non-operator grant)
	// and worksAt building A only — no identifiedBy link to EITHER
	// providerA or providerB, so actor_bound_to_appointment_provider answers
	// False for both and only the workplace walk can decide; no operator
	// holdsRole link, so actor_holds_operator resolves False too.
	_, buildingAID, _ := substrate.ParseVertexKey(buildingA)
	clSeedLink(t, ctx, conn,
		"lnk.identity."+reActorID+".worksAt.building."+buildingAID,
		reActorKey, buildingA, "worksAt", "worksAt")

	apptAtA := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "recfapptA01", patientA, providerA, buildingA, processor.OutcomeAccepted)
	apptAtAKey := "vtx.appointment." + apptAtA
	apptAtB := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "recfapptB01", patientB, providerB, buildingB, processor.OutcomeAccepted)
	apptAtBKey := "vtx.appointment." + apptAtB

	// Both appointments start 2026-07-01T15:00Z; document at 15:35Z so the
	// visit has started (refuse_before_start).
	const after = "2026-07-01T15:35:00Z"

	got, why := submitRecordEncounterAs(t, ctx, conn, cp, cons, "recfrecA0001", apptAtAKey, after, reActorKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("unbound provider RecordEncounter at its OWN workplace = %v (%s), want Accepted "+
			"(the positive sibling — if this fails the negative below proves nothing)", got, why)
	}
	got, why = submitRecordEncounterAs(t, ctx, conn, cp, cons, "recfrecB0001", apptAtBKey, after, reActorKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("unbound provider RecordEncounter at ANOTHER building = %v, want Rejected", got)
	}
	if why == "" {
		t.Fatalf("expected a refusal reason")
	}

	docA := clReadDoc(t, ctx, conn, apptAtAKey+".documentation")
	dA, _ := docA["data"].(map[string]any)
	if dA["documentedAt"] != "2026-07-01T15:35:00Z" {
		t.Fatalf("accepted RecordEncounter at its OWN workplace: documentedAt = %v, want 2026-07-01T15:35:00Z", dA["documentedAt"])
	}
	if !clMissing(t, ctx, conn, apptAtBKey+".documentation") {
		t.Fatalf("the denied cross-building RecordEncounter must not have written apptAtB's .documentation; it must be denied before any mutation")
	}
}
