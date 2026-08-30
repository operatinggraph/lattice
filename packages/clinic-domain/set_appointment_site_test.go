// SetAppointmentSite integration tests for the clinic-domain Capability
// Package — the human-facing manual counterpart to BackfillAppointmentSite
// (backfill_site_test.go): a staffer or the appointment's own bound provider
// supplies the site BackfillAppointmentSite could never resolve on its own
// (a provider practising at zero/multiple sites, or now tombstoned).
//
// Same external test package / harness as integration_test.go and
// site_integration_test.go: seed the kernel, install rbac+identity+hygiene +
// location-domain + clinic-domain, then submit SetAppointmentSite and assert
// the committed atSite link (or its deliberate absence).
//
// Coverage:
//  1. TestClinic_SetAppointmentSite_Fills        — a staffer fills a missing site
//  2. TestClinic_SetAppointmentSite_NoopAlreadyHasSite — no-ops when the appointment already carries a live atSite link
//  3. TestClinic_SetAppointmentSite_RejectsProviderNotAtSite — hard-validated exactly like CreateAppointment's own site branch
//  4. TestClinic_SetAppointmentSite_ConfinedToWorkplace — the NEW enforcement point: a front-desk actor may set the site only on an appointment whose provider practises at a building it worksAt
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

// submitSetAppointmentSiteAs submits SetAppointmentSite as an arbitrary actor
// on the standing path (no authContext) — mirrors submitCreateApptAs
// (frontdesk_confinement_test.go), declaring exactly what a staff caller
// declares: the appointment plus the site's own optional-read idiom
// (require_site_membership reads both on demand, mirroring CreateAppointment's
// site branch).
func submitSetAppointmentSiteAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, apptKey, siteKey, providerKey, actorKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetAppointmentSite",
		Actor:         actorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       json.RawMessage(`{"appointmentKey":"` + apptKey + `","site":"` + siteKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{apptKey},
			OptionalReads: []string{siteKey, practicesAtLinkKey(providerKey, siteKey)},
			Enumerations:  testutil.DeclaredEnumerations("SetAppointmentSite", actorKey, clinicdomain.OpMetas()),
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestClinic_SetAppointmentSite_Fills proves the core manual path: a staffer
// supplies the site on an appointment booked with none, writing the same
// atSite link mutation BackfillAppointmentSite/CreateAppointment's site
// branch would.
func TestClinic_SetAppointmentSite_Fills(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "sas-fills")

	patientKey := createPatient(t, ctx, conn, cp, cons, "sasfpat00001", "Sam Setsite")
	providerKey := createProvider(t, ctx, conn, cp, cons, "sasfprv00001", "Dr. Set Site", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "sasfbld00001")
	assignProviderSite(t, ctx, conn, cp, cons, "sasfasg00001", providerKey, buildingKey, processor.OutcomeAccepted)

	apptID := clSubmit(t, ctx, conn, cp, cons, "sasfappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	lk := atSiteLinkKey(apptKey, buildingKey)
	if !clMissing(t, ctx, conn, lk) {
		t.Fatalf("appointment must not carry an atSite link before SetAppointmentSite")
	}

	submitSetAppointmentSiteAs(t, ctx, conn, cp, cons, "sasfset00001", apptKey, buildingKey, providerKey, clStaffActorKey, processor.OutcomeAccepted)

	doc := clReadDoc(t, ctx, conn, lk)
	if doc["class"] != "atSite" {
		t.Fatalf("atSite link class = %v, want atSite", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("atSite link should be alive; got isDeleted=%v", del)
	}
	if tv, _ := doc["targetVertex"].(string); tv != buildingKey {
		t.Fatalf("atSite link target = %q, want %q", tv, buildingKey)
	}
}

// TestClinic_SetAppointmentSite_NoopAlreadyHasSite proves reassignment is out
// of scope: dispatching against an appointment that already carries a live
// atSite link leaves it untouched rather than rewriting it, mirroring
// BackfillAppointmentSite's own already-present no-op.
func TestClinic_SetAppointmentSite_NoopAlreadyHasSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "sas-noop")

	patientKey := createPatient(t, ctx, conn, cp, cons, "sasnpat00001", "Nora Noop")
	providerKey := createProvider(t, ctx, conn, cp, cons, "sasnprv00001", "Dr. No-op", "Cardiology")
	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "sasnbldA0001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "sasnbldB0001")
	assignProviderSite(t, ctx, conn, cp, cons, "sasnasgA0001", providerKey, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "sasnasgB0001", providerKey, buildingB, processor.OutcomeAccepted)

	apptID := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "sasnappt0001", patientKey, providerKey, buildingA, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID
	lk := atSiteLinkKey(apptKey, buildingA)

	// Attempt to "correct" it to buildingB — must no-op, leaving the ORIGINAL
	// site untouched (reassignment is out of scope; only a missing site is
	// filled).
	submitSetAppointmentSiteAs(t, ctx, conn, cp, cons, "sasnset00001", apptKey, buildingB, providerKey, clStaffActorKey, processor.OutcomeAccepted)

	after := clReadDoc(t, ctx, conn, lk)
	if del, _ := after["isDeleted"].(bool); del {
		t.Fatalf("original atSite link should remain alive after a no-op; got isDeleted=%v", del)
	}
	if tv, _ := after["targetVertex"].(string); tv != buildingA {
		t.Fatalf("atSite link target changed by a no-op SetAppointmentSite: got %q, want %q (unchanged)", tv, buildingA)
	}
	if !clMissing(t, ctx, conn, atSiteLinkKey(apptKey, buildingB)) {
		t.Fatalf("no second atSite link should be written for buildingB")
	}
}

// TestClinic_SetAppointmentSite_RejectsProviderNotAtSite proves the site is
// HARD-validated exactly like CreateAppointment's own site branch
// (TestClinic_CreateAppointment_RejectsProviderNotAtSite) — a site the
// appointment's provider does not practicesAt is rejected, writing no link.
func TestClinic_SetAppointmentSite_RejectsProviderNotAtSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "sas-wrongsite")

	patientKey := createPatient(t, ctx, conn, cp, cons, "saswpat00001", "Wanda Wrongsite")
	providerKey := createProvider(t, ctx, conn, cp, cons, "saswprv00001", "Dr. Not Here", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "saswbld00001")
	// Deliberately no AssignProviderSite — the provider does not practice here.

	apptID := clSubmit(t, ctx, conn, cp, cons, "saswappt0001", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerKey+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerKey}, processor.OutcomeAccepted)
	apptKey := "vtx.appointment." + apptID

	submitSetAppointmentSiteAs(t, ctx, conn, cp, cons, "saswset00001", apptKey, buildingKey, providerKey, clStaffActorKey, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, atSiteLinkKey(apptKey, buildingKey)) {
		t.Fatalf("no atSite link should be committed when the provider is not assigned to the requested site")
	}
}

// --- workplace confinement ---------------------------------------------

const (
	sasActorID  = "CLSASCNFACTRHJKMNPQR"
	sasActorKey = "vtx.identity." + sasActorID
	sasCapKey   = "cap.identity." + sasActorID
)

// sasCapDoc grants the front-desk actor the SAME scope=any surface a real
// clinic-app staff session holds for SetAppointmentSite ONLY — a dedicated
// capability doc rather than widening frontdesk_confinement_test.go's shared
// fdCapDoc, so this file's guarantee stays isolated from that file's own
// CreateAppointment/CreatePatient-specific assertions.
func sasCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    sasCapKey,
		Actor:                  sasActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{sasActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetAppointmentSite", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestClinic_SetAppointmentSite_ConfinedToWorkplace is the guarantee for the
// NEW enforcement point this op adds: one front-desk actor, one scope=any
// SetAppointmentSite grant, accepted on an appointment whose provider
// practises at the building it worksAt and rejected on one that doesn't —
// mirroring TestFrontDesk_ConfinedToWorkplace (frontdesk_confinement_test.go)
// for CreateAppointment, proving the SAME require_workplace/appointment_sites
// idiom this op copies actually fires here too, not just in the op it was
// copied from.
func TestClinic_SetAppointmentSite_ConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, sasCapDoc())
	cp, cons := newClinicPipeline(t, ctx, conn, "sas-confine")

	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "sascfbldA001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "sascfbldB001")
	providerA := createProvider(t, ctx, conn, cp, cons, "sascfprvA001", "Dr. Own Turf", "Cardiology")
	providerB := createProvider(t, ctx, conn, cp, cons, "sascfprvB001", "Dr. Elsewhere", "Cardiology")
	assignProviderSite(t, ctx, conn, cp, cons, "sascfasgA001", providerA, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "sascfasgB001", providerB, buildingB, processor.OutcomeAccepted)
	patientKey := createPatient(t, ctx, conn, cp, cons, "sascfpat0001", "Con Finement")

	// The front-desk actor worksAt building A only — no operator holdsRole
	// link, so actor_holds_operator resolves False (cannot prove root).
	_, buildingAID, _ := substrate.ParseVertexKey(buildingA)
	clSeedLink(t, ctx, conn,
		"lnk.identity."+sasActorID+".worksAt.building."+buildingAID,
		sasActorKey, buildingA, "worksAt", "worksAt")

	apptAtA := clSubmit(t, ctx, conn, cp, cons, "sascfapptA01", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerA+`","startsAt":"2026-07-01T15:00:00Z","endsAt":"2026-07-01T15:30:00Z"}`,
		[]string{patientKey, providerA}, processor.OutcomeAccepted)
	apptAtAKey := "vtx.appointment." + apptAtA

	apptAtB := clSubmit(t, ctx, conn, cp, cons, "sascfapptB01", "CreateAppointment", "appointment",
		`{"patient":"`+patientKey+`","provider":"`+providerB+`","startsAt":"2026-07-01T16:00:00Z","endsAt":"2026-07-01T16:30:00Z"}`,
		[]string{patientKey, providerB}, processor.OutcomeAccepted)
	apptAtBKey := "vtx.appointment." + apptAtB

	submitSetAppointmentSiteAs(t, ctx, conn, cp, cons, "sascfsetA001", apptAtAKey, buildingA, providerA, sasActorKey, processor.OutcomeAccepted)
	submitSetAppointmentSiteAs(t, ctx, conn, cp, cons, "sascfsetB001", apptAtBKey, buildingB, providerB, sasActorKey, processor.OutcomeRejected)

	if clMissing(t, ctx, conn, atSiteLinkKey(apptAtAKey, buildingA)) {
		t.Fatalf("front-desk SetAppointmentSite on an appointment at its OWN workplace should have committed an atSite link")
	}
	if !clMissing(t, ctx, conn, atSiteLinkKey(apptAtBKey, buildingB)) {
		t.Fatalf("front-desk SetAppointmentSite on an appointment at ANOTHER building should NOT have committed an atSite link — the workplace confinement gate")
	}
}
