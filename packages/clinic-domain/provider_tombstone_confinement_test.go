package clinicdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// A tombstoned provider confers nothing — the resolver half of the workplace
// rule, for the ops that resolve their provider from the APPOINTMENT rather than
// from the payload.
//
// CreateAppointment and StartVisitSeries both validate the payload provider
// alive before confining, so a dead provider never reaches `sites_for_provider`
// there. RescheduleAppointment and SetAppointmentStatus do not: their provider
// comes from the appointment's own withProvider link, and nothing between the
// link and `practicesAt` had ever read the provider VERTEX. TombstoneProvider
// soft-deletes it with no cascade onto practicesAt, so a retired provider went
// on handing back the buildings it no longer practises at, and staff there kept
// authority over its appointments.
//
// Topology:
//
//	vtx.building.<B>
//	      ^ practicesAt        ^ worksAt
//	vtx.provider.<P>      vtx.identity.<actor>
//	      ^ withProvider
//	vtx.appointment.<A>
const (
	ptActorID  = "CLPTTMBACTRHJKMNPQRS"
	ptActorKey = "vtx.identity." + ptActorID
	ptCapKey   = "cap.identity." + ptActorID

	ptBuildingID = "CLPTTMBBLDGHJKMNPQRS"
	ptProviderID = "CLPTTMBPRVDHJKMNPQRS"
	ptPatientID  = "CLPTTMBPATNHJKMNPQRS"
	ptApptID     = "CLPTTMBAPPTHJKMNPQRS"

	ptBuildingKey = "vtx.building." + ptBuildingID
	ptProviderKey = "vtx.provider." + ptProviderID
	ptPatientKey  = "vtx.patient." + ptPatientID
	ptApptKey     = "vtx.appointment." + ptApptID
)

// ptCapDoc grants the front-desk actor SetAppointmentStatus at scope=any — the
// same row operator holds. Scope carries no platform-checked target, so whatever
// confines this actor lives entirely in the script.
func ptCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ptCapKey,
		Actor:                  ptActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ptActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetAppointmentStatus", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// seedProviderTombstoneTopology builds the world above with the provider ALIVE.
// The actor holds no operator role, so actor_holds_operator resolves False and
// it cannot prove root; it is not identifiedBy the provider either, so the
// bound-provider binder cannot answer for it and only the workplace walk can.
func seedProviderTombstoneTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	clSeedVertex(t, ctx, conn, ptBuildingKey, "location", false)
	clSeedVertex(t, ctx, conn, ptProviderKey, "provider", false)
	clSeedVertex(t, ctx, conn, ptPatientKey, "patient", false)
	clSeedVertex(t, ctx, conn, ptActorKey, "identity", false)

	clSeedVertex(t, ctx, conn, ptApptKey, "appointment", false)
	clSeedVertex(t, ctx, conn, ptApptKey+".status", "appointmentStatus", false)

	clSeedLink(t, ctx, conn,
		"lnk.provider."+ptProviderID+".practicesAt.building."+ptBuildingID,
		ptProviderKey, ptBuildingKey, "practicesAt", "practicesAt")
	clSeedLink(t, ctx, conn,
		"lnk.identity."+ptActorID+".worksAt.building."+ptBuildingID,
		ptActorKey, ptBuildingKey, "worksAt", "worksAt")
	clSeedLink(t, ctx, conn,
		"lnk.appointment."+ptApptID+".withProvider.provider."+ptProviderID,
		ptApptKey, ptProviderKey, "withProvider", "withProvider")
	clSeedLink(t, ctx, conn,
		"lnk.appointment."+ptApptID+".forPatient.patient."+ptPatientID,
		ptApptKey, ptPatientKey, "forPatient", "forPatient")

	testutil.SeedHoldsRole(t, ctx, conn, ptActorKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "frontOfHouse"))
	testutil.SeedCapDoc(t, ctx, conn, ptCapDoc())
}

// submitSetStatusAs submits SetAppointmentStatus as an arbitrary actor and
// returns the outcome plus the script's own failure text, so a rejection can be
// attributed to the confinement guard rather than to any other gate.
func submitSetStatusAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, status, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"appointmentKey": ptApptKey, "status": status})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetAppointmentStatus",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{ptApptKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return outcome, failure
}

// TestWorkplace_TombstonedProviderConfersNothing walks one staffer through the
// same write either side of TombstoneProvider. The appointment, its links and
// the building are untouched throughout — the only thing that changes is the
// provider's own isDeleted, which is precisely what nothing used to read.
func TestWorkplace_TombstonedProviderConfersNothing(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	seedProviderTombstoneTopology(t, ctx, conn)
	cp, cons := newClinicPipeline(t, ctx, conn, "ptdead")

	// POSITIVE SIBLING: while the provider is alive, its practicesAt building is
	// one the staffer worksAt, so confinement passes.
	got, why := submitSetStatusAs(t, ctx, conn, cp, cons, "ptdeadlive01", "confirmed", ptActorKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff SetAppointmentStatus with a LIVE provider at its own building = %v (%s), "+
			"want Accepted (the positive sibling — if this fails the negative proves nothing)", got, why)
	}
	status := clReadDoc(t, ctx, conn, ptApptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "confirmed" {
		t.Fatalf("after the accepted SetAppointmentStatus, status = %v, want confirmed", st["value"])
	}

	// Retire the provider. Its practicesAt link stays live and so does the
	// appointment's withProvider link — TombstoneProvider cascades to neither.
	clSeedVertex(t, ctx, conn, ptProviderKey, "provider", true)

	got, why = submitSetStatusAs(t, ctx, conn, cp, cons, "ptdeaddead01", "checkedIn", ptActorKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff SetAppointmentStatus with a TOMBSTONED provider = %v, want Rejected — "+
			"a retired provider must not keep conferring the buildings it practised at", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("the tombstoned-provider denial said %q, want the confinement guard's message", why)
	}

	// The write really was denied before any mutation: the status the accepted
	// call wrote is still what stands.
	status = clReadDoc(t, ctx, conn, ptApptKey+".status")
	if st, _ := status["data"].(map[string]any); st["value"] != "confirmed" {
		t.Errorf("the denied SetAppointmentStatus moved status to %v; it must be denied before any mutation", st["value"])
	}
}
