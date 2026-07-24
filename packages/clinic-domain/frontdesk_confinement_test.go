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
)

// Front-desk write confinement for CreateAppointment / CreatePatient
// (persona-worlds-design.md §7.1 grants audit, Fire W1 Inc 2a).
//
// Inc 1 deleted the root-equivalent staff mint, so a clinic "staff" session is
// now a genuine frontOfHouse identity holding its grants at scope=any, exactly
// as operator does — the capability plane cannot tell the two apart (scope is
// only `any` or `self`, Contract #6). Confinement therefore lives in the op
// script: a front-desk caller may book only with a provider practising at a
// building it worksAt (mirroring RescheduleAppointment / SetAppointmentStatus),
// while CreatePatient is deliberately unconfined (a patient vertex is
// practice-wide — no building to confine to).
//
// Topology every vector builds:
//
//	vtx.building.<A>            vtx.building.<B>
//	      ^ practicesAt               ^ practicesAt
//	vtx.provider.<PA>          vtx.provider.<PB>
//
// The front-desk identity worksAt building A only, holds no operator role, and
// so cannot prove root.
const (
	fdActorID  = "CLFDCNFACTRHJKMNPQRS"
	fdActorKey = "vtx.identity." + fdActorID
	fdCapKey   = "cap.identity." + fdActorID

	fdBuildingAID = "CLFDBLDGAHJKMNPQRSTV"
	fdBuildingBID = "CLFDBLDGBHJKMNPQRSTV"
	fdProviderAID = "CLFDPRVDRAHJKMNPQRST"
	fdProviderBID = "CLFDPRVDRBHJKMNPQRST"
	fdPatientID   = "CLFDPATNTHJKMNPQRSTV"
	fdVictimID    = "CLFDVCTMHJKMNPQRSTVW"

	fdBuildingAKey = "vtx.building." + fdBuildingAID
	fdBuildingBKey = "vtx.building." + fdBuildingBID
	fdProviderAKey = "vtx.provider." + fdProviderAID
	fdProviderBKey = "vtx.provider." + fdProviderBID
	fdPatientKey   = "vtx.patient." + fdPatientID
	// fdVictimKey is the identity fdPatient is identifiedBy — the target a
	// forged-authContext attack names to try to skip workplace confinement.
	fdVictimKey        = "vtx.identity." + fdVictimID
	fdPatientIDLinkKey = "lnk.patient." + fdPatientID + ".identifiedBy.identity." + fdVictimID
)

// fdCapDoc grants the front-desk actor the same scope=any book/register surface
// operator holds — the point of the confinement test is that the capability
// plane cannot distinguish staff from root, so if the boundary holds it holds
// entirely inside the script.
func fdCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    fdCapKey,
		Actor:                  fdActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{fdActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAppointment", Scope: "any"},
			{OperationType: "CreatePatient", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// seedFrontDeskTopology builds the two-building world above: provider PA
// practises at building A, provider PB at building B, one patient, and the
// front-desk identity worksAt building A only (no operator holdsRole link, so
// actor_holds_operator resolves False — the actor cannot prove root).
func seedFrontDeskTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	clSeedVertex(t, ctx, conn, fdBuildingAKey, "location", false)
	clSeedVertex(t, ctx, conn, fdBuildingBKey, "location", false)
	clSeedVertex(t, ctx, conn, fdProviderAKey, "provider", false)
	clSeedVertex(t, ctx, conn, fdProviderBKey, "provider", false)
	clSeedVertex(t, ctx, conn, fdPatientKey, "patient", false)

	clSeedLink(t, ctx, conn,
		"lnk.provider."+fdProviderAID+".practicesAt.building."+fdBuildingAID,
		fdProviderAKey, fdBuildingAKey, "practicesAt", "practicesAt")
	clSeedLink(t, ctx, conn,
		"lnk.provider."+fdProviderBID+".practicesAt.building."+fdBuildingBID,
		fdProviderBKey, fdBuildingBKey, "practicesAt", "practicesAt")

	clSeedLink(t, ctx, conn,
		"lnk.identity."+fdActorID+".worksAt.building."+fdBuildingAID,
		fdActorKey, fdBuildingAKey, "worksAt", "worksAt")

	// The patient is identifiedBy a victim identity — the real link a
	// forged-authContext-target attack points at to try to wear the consumer
	// self-book exemption.
	clSeedVertex(t, ctx, conn, fdVictimKey, "identity", false)
	clSeedLink(t, ctx, conn, fdPatientIDLinkKey, fdPatientKey, fdVictimKey, "identifiedBy", "identifiedBy")
}

// submitCreateApptAs books an appointment as an arbitrary actor on the standing
// path (no authContext), declaring exactly what a staff caller declares.
func submitCreateApptAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey, actorKey, startsAt, endsAt string) processor.MessageOutcome {
	t.Helper()
	payload := `{"patient":"` + fdPatientKey + `","provider":"` + providerKey +
		`","startsAt":"` + startsAt + `","endsAt":"` + endsAt + `","reason":"front-desk booking"}`
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{fdPatientKey, providerKey}},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// submitCreateApptWithTargetAs books an appointment as an arbitrary actor while
// attaching an attacker-chosen authContext.target — the vector that probes
// whether a scope=any staff caller can forge the consumer self-book exemption to
// skip workplace confinement.
func submitCreateApptWithTargetAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey, actorKey, target, starts, ends string, optionalReads []string) processor.MessageOutcome {
	t.Helper()
	payload := `{"patient":"` + fdPatientKey + `","provider":"` + providerKey +
		`","startsAt":"` + starts + `","endsAt":"` + ends + `","reason":"forged-target probe"}`
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{fdPatientKey, providerKey}, OptionalReads: optionalReads},
		AuthContext:   &processor.AuthContext{Target: target},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// submitCreatePatientAs registers a patient as an arbitrary actor.
func submitCreatePatientAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreatePatient",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "patient",
		Payload:       json.RawMessage(`{"fullName":"Walk-in Patient"}`),
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestFrontDesk_ConfinedToWorkplace is the Inc 2a guarantee: one front-desk
// actor, one scope=any CreateAppointment grant, accepted booking with a provider
// at the building it worksAt and rejected with one it does not — the multi-org
// gate, mirroring café's OpenTab confinement.
func TestFrontDesk_ConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "fdconfine")
	seedFrontDeskTopology(t, ctx, conn)

	// Distinct slots so the negative vector can be rejected ONLY by the workplace
	// guard — a shared patient slot would trip PatientDoubleBook and pass the
	// test for the wrong reason (a negative test that passes on any rejection).
	if got := submitCreateApptAs(t, ctx, conn, cp, cons, "fdok00000000000001", fdProviderAKey, fdActorKey,
		"2026-07-01T15:00:00Z", "2026-07-01T15:30:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk CreateAppointment with a provider at its OWN workplace = %v, want Accepted", got)
	}
	if got := submitCreateApptAs(t, ctx, conn, cp, cons, "fdno00000000000002", fdProviderBKey, fdActorKey,
		"2026-07-01T16:00:00Z", "2026-07-01T16:30:00Z"); got != processor.OutcomeRejected {
		t.Fatalf("front-desk CreateAppointment with a provider at ANOTHER building = %v, want Rejected — the multi-org gate", got)
	}
}

// TestFrontDesk_ForgedTargetCannotSkipConfinement is the security regression the
// adversarial review surfaced: step 3 authorizes a scope=any grant WITHOUT
// inspecting authContext.target, and the Gateway forwards the client's
// authContext verbatim — so a front-desk actor holding CreateAppointment
// scope=any could attach an arbitrary target and, if workplace_exempt() keyed off
// "target present", skip the workplace guard and book cross-building. Both forgery
// shapes must be rejected:
//
//	(a) target = the patient's real linked identity (!= actor) — must NOT exempt.
//	(b) target = the caller's own actor — exempts, but the op's identifiedBy check
//	    then binds the patient to the caller's identity, so booking a patient the
//	    caller does not own still fails.
func TestFrontDesk_ForgedTargetCannotSkipConfinement(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "fdforged")
	seedFrontDeskTopology(t, ctx, conn)

	// (a) target = the patient's linked identity, provider at building B (NOT the
	// front desk's workplace). Under the fixed guard the forged target != actor, so
	// the workplace check is NOT skipped and confinement rejects the cross-building
	// booking. (Under the pre-fix "target != ''" guard this was Accepted.)
	if got := submitCreateApptWithTargetAs(t, ctx, conn, cp, cons, "fdfrga00000000001",
		fdProviderBKey, fdActorKey, fdVictimKey, "2026-07-01T15:00:00Z", "2026-07-01T15:30:00Z",
		[]string{fdPatientIDLinkKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk CreateAppointment cross-building with a forged authContext.target = %v, want Rejected — forging the consumer exemption must not skip workplace confinement", got)
	}

	// (b) target = the caller's OWN actor identity: workplace_exempt() is satisfied
	// (target == actor), but the patient is identifiedBy the victim, not the caller,
	// so the op's own identifiedBy check rejects the booking of a patient the caller
	// does not own.
	if got := submitCreateApptWithTargetAs(t, ctx, conn, cp, cons, "fdfrgb00000000002",
		fdProviderBKey, fdActorKey, fdActorKey, "2026-07-01T16:00:00Z", "2026-07-01T16:30:00Z",
		nil); got != processor.OutcomeRejected {
		t.Fatalf("front-desk CreateAppointment with target = own actor for a patient it does not own = %v, want Rejected", got)
	}
}

// TestFrontDesk_OperatorUnconfined proves the guard leaves root alone: the
// operator actor holds no worksAt link at all and books at either building.
func TestFrontDesk_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "fdoper")
	seedFrontDeskTopology(t, ctx, conn)

	if got := submitCreateApptAs(t, ctx, conn, cp, cons, "fdopa000000000001", fdProviderAKey, clStaffActorKey,
		"2026-07-01T15:00:00Z", "2026-07-01T15:30:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateAppointment at building A = %v, want Accepted (root stays unconfined)", got)
	}
	if got := submitCreateApptAs(t, ctx, conn, cp, cons, "fdopb000000000002", fdProviderBKey, clStaffActorKey,
		"2026-07-01T16:00:00Z", "2026-07-01T16:30:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateAppointment at building B = %v, want Accepted (root stays unconfined)", got)
	}
}

// TestFrontDesk_RegisterPatientUnconfined pins the design decision that
// CreatePatient carries no workplace confinement: a patient vertex is
// practice-wide, so front-desk registration is accepted with no worksAt bearing
// on it — the register half of the front-desk service surface.
func TestFrontDesk_RegisterPatientUnconfined(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "fdregister")
	seedFrontDeskTopology(t, ctx, conn)

	if got := submitCreatePatientAs(t, ctx, conn, cp, cons, "fdreg000000000001", fdActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk CreatePatient = %v, want Accepted (registration is unconfined)", got)
	}
}
