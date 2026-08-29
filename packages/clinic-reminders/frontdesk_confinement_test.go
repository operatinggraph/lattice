// Front-desk (frontOfHouse) write confinement for the staff visit-series ops
// StartVisitSeries / PauseVisitSeries / ResumeVisitSeries / EndVisitSeries — the
// four ops the clinic front-desk Follow-ups tab submits (persona-worlds-design.md §7.1 grants
// audit). The capability plane cannot tell a frontOfHouse actor from operator
// (scope is only `any` or `self`, Contract #6), so confinement lives in the op
// script: a front-desk caller may act only on a series whose provider practises
// at a building it worksAt (mirrors clinic-domain's CreateAppointment).
//
// The load-bearing divergence from clinic-domain's appointment guard: these ops
// carry NO consumer/scope=self grant and NO identifiedBy ownership backstop, so
// they call enforce_workplace, not require_workplace — root is the only
// exemption, and there is no authContextTarget==actor self exemption anywhere on
// the path. TestFrontDesk_VisitSeries_ForgedTarget*
// pins exactly that: a front-desk actor that forges target==actor is STILL
// confined (under a copied appointment guard it would have skipped confinement).
//
// Topology every vector builds:
//
//	vtx.building.<A>            vtx.building.<B>
//	      ^ practicesAt               ^ practicesAt
//	vtx.provider.<PA>          vtx.provider.<PB>
//
// The front-desk identity worksAt building A only and holds no operator holdsRole
// link, so actor_holds_operator resolves False — it cannot prove root.
package clinicreminders_test

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

const (
	fdrActorID  = "CRFDVSACTRHJKMNPQRST"
	fdrActorKey = "vtx.identity." + fdrActorID
	fdrCapKey   = "cap.identity." + fdrActorID

	fdrBuildingAID = "CRFDBLDGAHJKMNPQRSTV"
	fdrBuildingBID = "CRFDBLDGBHJKMNPQRSTV"
	fdrProviderAID = "CRFDPRVDRAHJKMNPQRST"
	fdrProviderBID = "CRFDPRVDRBHJKMNPQRST"
	fdrPatientID   = "CRFDPATNTHJKMNPQRSTV"

	fdrBuildingAKey = "vtx.building." + fdrBuildingAID
	fdrBuildingBKey = "vtx.building." + fdrBuildingBID
	fdrProviderAKey = "vtx.provider." + fdrProviderAID
	fdrProviderBKey = "vtx.provider." + fdrProviderBID
	fdrPatientKey   = "vtx.patient." + fdrPatientID
)

// fdrCapDoc grants the front-desk actor the same scope=any visit-series surface
// operator holds — the point of the test is that the capability plane cannot
// distinguish staff from root, so if the boundary holds it holds entirely inside
// the script.
func fdrCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    fdrCapKey,
		Actor:                  fdrActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{fdrActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "StartVisitSeries", Scope: "any"},
			{OperationType: "PauseVisitSeries", Scope: "any"},
			{OperationType: "ResumeVisitSeries", Scope: "any"},
			{OperationType: "EndVisitSeries", Scope: "any"},
			{OperationType: "SetVisitSeriesSite", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// remSeedVertex writes an alive vertex doc straight to Core KV (the confinement
// guard only reads links, but StartVisitSeries validates the patient/provider
// endpoints are alive + correctly classed, so those must be real vertices).
func remSeedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

// seedRemFrontDeskTopology builds the two-building world: provider PA practises
// at building A, PB at building B, one patient, and the front-desk identity
// worksAt building A only (no operator holdsRole link).
func seedRemFrontDeskTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	remSeedVertex(t, ctx, conn, fdrBuildingAKey, "location")
	remSeedVertex(t, ctx, conn, fdrBuildingBKey, "location")
	remSeedVertex(t, ctx, conn, fdrProviderAKey, "provider")
	remSeedVertex(t, ctx, conn, fdrProviderBKey, "provider")
	remSeedVertex(t, ctx, conn, fdrPatientKey, "patient")

	testutil.SeedLink(t, ctx, conn,
		"lnk.provider."+fdrProviderAID+".practicesAt.building."+fdrBuildingAID,
		"practicesAt", fdrProviderAKey, fdrBuildingAKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.provider."+fdrProviderBID+".practicesAt.building."+fdrBuildingBID,
		"practicesAt", fdrProviderBKey, fdrBuildingBKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+fdrActorID+".worksAt.building."+fdrBuildingAID,
		"worksAt", fdrActorKey, fdrBuildingAKey)
}

// crDriveAs submits an op as an arbitrary actor (optionally with a forged
// authContext.target) and RETURNS the outcome (want="" tells DriveOne not to
// assert), so a vector can distinguish Accepted from Rejected itself.
func crDriveAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op, class, payload, actorKey, target string, reads []string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         actorKey,
		SubmittedAt:   crSubmittedAnchor,
		Class:         class,
		Payload:       json.RawMessage(payload),
	}
	if len(reads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads}
	}
	if target != "" {
		env.AuthContext = &processor.AuthContext{Target: target}
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

func startSeriesPayload(providerKey string) string {
	return `{"patientKey":"` + fdrPatientKey + `","providerKey":"` + providerKey +
		`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`
}

// TestFrontDesk_VisitSeries_ConfinedToWorkplace is the guarantee: one front-desk
// actor, one scope=any StartVisitSeries grant, accepted with a provider at the
// building it worksAt and rejected with one it does not — the multi-org gate.
// The two vectors use DIFFERENT providers (so different per-(patient,provider)
// guards), so the cross-building call can be rejected ONLY by the workplace guard,
// never by ActiveVisitSeriesExists (a negative test that passes for the wrong
// reason).
func TestFrontDesk_VisitSeries_ConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdrCapDoc())
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdrconfine", Instance: "cr-fdrconfine"})
	seedRemFrontDeskTopology(t, ctx, conn)

	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrok0000000000001", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderAKey), fdrActorKey, "", []string{fdrPatientKey, fdrProviderAKey}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk StartVisitSeries with a provider at its OWN workplace = %v, want Accepted", got)
	}
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrno0000000000002", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), fdrActorKey, "", []string{fdrPatientKey, fdrProviderBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk StartVisitSeries with a provider at ANOTHER building = %v, want Rejected — the multi-org gate", got)
	}
}

// TestFrontDesk_VisitSeries_ForgedTargetCannotSkipConfinement is the security
// regression pinning the operator-exempt-only divergence. A front-desk actor
// holding StartVisitSeries scope=any can attach ANY authContext.target (step 3
// authorizes scope=any without inspecting it; the Gateway forwards it verbatim).
// Under a copied appointment guard, target==actor would satisfy workplace_exempt
// and skip confinement — but StartVisitSeries has no consumer self path and no
// identifiedBy backstop, so BOTH forgery shapes with a cross-building provider
// must be Rejected.
func TestFrontDesk_VisitSeries_ForgedTargetCannotSkipConfinement(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdrCapDoc())
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdrforged", Instance: "cr-fdrforged"})
	seedRemFrontDeskTopology(t, ctx, conn)

	// (a) target = the caller's OWN actor: the exemption a copied appointment
	// guard would grant. Here it must NOT skip the workplace check.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrfrga0000000001", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), fdrActorKey, fdrActorKey, []string{fdrPatientKey, fdrProviderBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk StartVisitSeries cross-building with a forged target==actor = %v, want Rejected — no self exemption on a staff-only op", got)
	}
	// (b) target = an arbitrary other identity: likewise no exemption.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrfrgb0000000002", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), fdrActorKey, "vtx.identity."+fdrPatientID, []string{fdrPatientKey, fdrProviderBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk StartVisitSeries cross-building with a forged arbitrary target = %v, want Rejected", got)
	}
	// (c) POSITIVE control: the SAME forged target==actor but with a SAME-building
	// (A) provider is ACCEPTED. This attributes the two rejections above to the
	// SCRIPT's workplace guard, not to any auth-plane handling of a target on a
	// scope=any grant: step 3 authorizes scope=any without inspecting the target
	// and the Gateway forwards it verbatim, so the forged target reaches the
	// script inert — and confinement admits it precisely because provider A is at
	// the actor's workplace. Were the forge instead rejected upstream, this vector
	// would reject too.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrfrgc0000000003", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderAKey), fdrActorKey, fdrActorKey, []string{fdrPatientKey, fdrProviderAKey}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk StartVisitSeries at its OWN building with a forged target==actor = %v, want Accepted — the forged target is forwarded verbatim and the operator-exempt-only script guard governs", got)
	}
}

// TestFrontDesk_VisitSeries_OperatorUnconfined proves the guard leaves root
// alone: the operator actor holds no worksAt link and starts a series with a
// provider at either building.
func TestFrontDesk_VisitSeries_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdroper", Instance: "cr-fdroper"})
	seedRemFrontDeskTopology(t, ctx, conn)

	if got := crDriveAs(t, ctx, conn, cp, cons, "fdropa0000000001", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), crStaffActorKey, "", []string{fdrPatientKey, fdrProviderBKey}); got != processor.OutcomeAccepted {
		t.Fatalf("operator StartVisitSeries with a provider at building B = %v, want Accepted (root stays unconfined)", got)
	}
}

// TestFrontDesk_VisitSeries_PauseResumeConfined proves the confinement extends to
// the tab's lifecycle ops, which carry only seriesKey (provider resolved off the
// series' own withProvider link): a front-desk actor may pause/resume a series
// whose provider is at its workplace and is rejected for one that is not.
func TestFrontDesk_VisitSeries_PauseResumeConfined(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdrCapDoc())
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdrpause", Instance: "cr-fdrpause"})
	seedRemFrontDeskTopology(t, ctx, conn)

	// Operator (unconfined) starts both series so Pause/Resume have targets. Same
	// patient, different providers -> distinct per-(patient,provider) guards, both
	// accepted.
	seriesAID := crSubmit(t, ctx, conn, cp, cons, "fdrpaseedA", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderAKey), []string{fdrPatientKey, fdrProviderAKey}, processor.OutcomeAccepted)
	seriesAKey := "vtx.visitseries." + seriesAID
	seriesBID := crSubmit(t, ctx, conn, cp, cons, "fdrpaseedB", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), []string{fdrPatientKey, fdrProviderBKey}, processor.OutcomeAccepted)
	seriesBKey := "vtx.visitseries." + seriesBID

	// Front-desk may pause/resume the building-A series (its workplace)...
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrpauseA1", "PauseVisitSeries", "",
		`{"seriesKey":"`+seriesAKey+`"}`, fdrActorKey, "", []string{seriesAKey}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk PauseVisitSeries on a building-A series = %v, want Accepted", got)
	}
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrresumeA", "ResumeVisitSeries", "",
		`{"seriesKey":"`+seriesAKey+`"}`, fdrActorKey, "", []string{seriesAKey}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk ResumeVisitSeries on a building-A series = %v, want Accepted", got)
	}
	// ...but not the building-B series.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrpauseB1", "PauseVisitSeries", "",
		`{"seriesKey":"`+seriesBKey+`"}`, fdrActorKey, "", []string{seriesBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk PauseVisitSeries on a building-B series = %v, want Rejected — the multi-org gate", got)
	}
}

// TestFrontDesk_VisitSeries_EndConfined mirrors PauseResumeConfined for the new
// EndVisitSeries op: a front-desk actor may end a series whose provider is at its
// workplace and is rejected for one that is not. EndVisitSeries additionally
// declares its own .series aspect as a read (unlike Pause/Resume), so both
// vectors carry it in ContextHint.Reads.
func TestFrontDesk_VisitSeries_EndConfined(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdrCapDoc())
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdrend", Instance: "cr-fdrend"})
	seedRemFrontDeskTopology(t, ctx, conn)

	seriesAID := crSubmit(t, ctx, conn, cp, cons, "fdreseedA", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderAKey), []string{fdrPatientKey, fdrProviderAKey}, processor.OutcomeAccepted)
	seriesAKey := "vtx.visitseries." + seriesAID
	seriesBID := crSubmit(t, ctx, conn, cp, cons, "fdreseedB", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), []string{fdrPatientKey, fdrProviderBKey}, processor.OutcomeAccepted)
	seriesBKey := "vtx.visitseries." + seriesBID

	// Front-desk may end the building-A series (its workplace)...
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrendA1", "EndVisitSeries", "",
		`{"seriesKey":"`+seriesAKey+`"}`, fdrActorKey, "", []string{seriesAKey, seriesAKey + ".series"}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk EndVisitSeries on a building-A series = %v, want Accepted", got)
	}
	// ...but not the building-B series.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrendB1", "EndVisitSeries", "",
		`{"seriesKey":"`+seriesBKey+`"}`, fdrActorKey, "", []string{seriesBKey, seriesBKey + ".series"}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk EndVisitSeries on a building-B series = %v, want Rejected — the multi-org gate", got)
	}
}

// TestFrontDesk_VisitSeries_SetSiteConfined extends the same guarantee to
// SetVisitSeriesSite, the one site op a human submits: a front-desk actor may
// site a series whose provider practises at its workplace and is rejected for
// one that does not. The op resolves its confining set and its site whitelist
// from the SAME sites_for_provider walk, so a site the guard would not have
// admitted can never be a site the caller may choose.
func TestFrontDesk_VisitSeries_SetSiteConfined(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, fdrCapDoc())
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "fdrsite", Instance: "cr-fdrsite"})
	seedRemFrontDeskTopology(t, ctx, conn)

	// Operator (unconfined) starts both series so the site vectors have targets.
	seriesAID := crSubmit(t, ctx, conn, cp, cons, "fdrsseedA", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderAKey), []string{fdrPatientKey, fdrProviderAKey}, processor.OutcomeAccepted)
	seriesAKey := "vtx.visitseries." + seriesAID
	seriesBID := crSubmit(t, ctx, conn, cp, cons, "fdrsseedB", "StartVisitSeries", "visitseries",
		startSeriesPayload(fdrProviderBKey), []string{fdrPatientKey, fdrProviderBKey}, processor.OutcomeAccepted)
	seriesBKey := "vtx.visitseries." + seriesBID

	// Front-desk may site the building-A series (its workplace)...
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrsiteA1", "SetVisitSeriesSite", "",
		`{"seriesKey":"`+seriesAKey+`","site":"`+fdrBuildingAKey+`"}`, fdrActorKey, "", []string{seriesAKey}); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk SetVisitSeriesSite on a building-A series = %v, want Accepted", got)
	}
	// ...but not the building-B series, even naming B's own legitimate site.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrsiteB1", "SetVisitSeriesSite", "",
		`{"seriesKey":"`+seriesBKey+`","site":"`+fdrBuildingBKey+`"}`, fdrActorKey, "", []string{seriesBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk SetVisitSeriesSite on a building-B series = %v, want Rejected — the multi-org gate", got)
	}
	// A forged target==actor buys nothing here either: these ops are
	// operator-exempt only.
	if got := crDriveAs(t, ctx, conn, cp, cons, "fdrsiteB2", "SetVisitSeriesSite", "",
		`{"seriesKey":"`+seriesBKey+`","site":"`+fdrBuildingBKey+`"}`, fdrActorKey, fdrActorKey, []string{seriesBKey}); got != processor.OutcomeRejected {
		t.Fatalf("front-desk SetVisitSeriesSite cross-building with a forged target==actor = %v, want Rejected", got)
	}
}
