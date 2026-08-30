// Multi-site integration tests for the clinic-domain Capability Package.
//
// External test package (clinicdomain_test), same harness as
// integration_test.go: seed the kernel, install rbac+identity+hygiene +
// location-domain + clinic-domain, then submit SetSiteProfile /
// AssignProviderSite / RemoveProviderSite and assert the committed Core-KV
// shape — the .site aspect on a location-domain building, and the
// provider→building practicesAt link (mirrors loftspace-domain's
// ownership_integration_test.go for AssignUnitOwner/RemoveUnitOwner).
//
// Coverage:
//  1. TestClinic_SetSiteProfile              — .site aspect committed with the supplied name
//  2. TestClinic_SetSiteProfileRejectsNonLocationBuilding — non-building key type rejected
//  3. TestClinic_AssignProviderSite           — practicesAt link committed alive, source=provider, target=building
//  4. TestClinic_AssignProviderSiteIdempotent — re-assign is a clean no-op
//  5. TestClinic_RemoveThenReassignProviderSite — remove tombstones; re-assign revives (CAS), alive again
//  6. TestClinic_AssignProviderSiteRejectsDeadProvider — tombstoned provider → Rejected, no link
//  7. TestClinic_ProviderMultipleSites         — one provider practicesAt two different buildings, both links alive
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

// practicesAtLinkKey is the deterministic per-(provider, building) practicesAt
// link key.
func practicesAtLinkKey(providerKey, buildingKey string) string {
	_, pid, _ := substrate.ParseVertexKey(providerKey)
	_, bid, _ := substrate.ParseVertexKey(buildingKey)
	return "lnk.provider." + pid + ".practicesAt.building." + bid
}

// assignProviderSite submits AssignProviderSite(provider, building) with the
// expected outcome. Both endpoints are listed in ContextHint.Reads
// (alive-checked in-script); the practicesAt link is read on demand.
func assignProviderSite(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey, buildingKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "AssignProviderSite",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "clinicSiteAssignment",
		Payload:       json.RawMessage(`{"provider":"` + providerKey + `","building":"` + buildingKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{providerKey, buildingKey},
			OptionalReads: []string{practicesAtLinkKey(providerKey, buildingKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// removeProviderSite submits RemoveProviderSite(provider, building). The link
// is read on demand (declared optionalReads — it may not exist, idempotent
// no-op).
func removeProviderSite(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey, buildingKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RemoveProviderSite",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "clinicSiteAssignment",
		Payload:       json.RawMessage(`{"provider":"` + providerKey + `","building":"` + buildingKey + `"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{practicesAtLinkKey(providerKey, buildingKey)}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

func TestClinic_SetSiteProfile(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "site-profile")

	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "spbldg0001")

	clSubmit(t, ctx, conn, cp, cons, "spset0001", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Downtown Clinic"}`,
		[]string{buildingKey}, processor.OutcomeAccepted)

	site := clReadDoc(t, ctx, conn, buildingKey+".site")
	if site["class"] != "clinicSiteProfile" {
		t.Fatalf("site class = %v, want clinicSiteProfile", site["class"])
	}
	if sd, _ := site["data"].(map[string]any); sd["name"] != "Downtown Clinic" {
		t.Fatalf("site name = %v, want Downtown Clinic", sd["name"])
	}

	// Full-replace upsert: re-running with a new name overwrites, not merges.
	clSubmit(t, ctx, conn, cp, cons, "spset0002", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Uptown Clinic"}`,
		[]string{buildingKey}, processor.OutcomeAccepted)
	site2 := clReadDoc(t, ctx, conn, buildingKey+".site")
	if sd, _ := site2["data"].(map[string]any); sd["name"] != "Uptown Clinic" {
		t.Fatalf("site name after re-set = %v, want Uptown Clinic", sd["name"])
	}
}

// clSubmitSiteProfileReason submits SetSiteProfile and returns the script's own
// failure message, so a rejection vector can name the guard it means to
// exercise. require_live_building is the SOLE guard on buildingKey, but a bare
// outcome check would still be satisfied by a malformed key or a hydration
// miss reaching step 6 first.
func clSubmitSiteProfileReason(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, buildingKey string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetSiteProfile",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "clinicSite",
		Payload:       json.RawMessage(`{"buildingKey":"` + buildingKey + `","name":"Ghost Clinic"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{buildingKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// TestClinic_SetSiteProfileRejectsNonLocationBuilding: a live vertex whose KEY
// TYPE SEGMENT is not `building` is rejected (NotALocation) and no aspect is
// committed.
//
// require_live_building is the SOLE type guard on buildingKey — nothing else in
// this op's script constrains it — so without this rejection SetSiteProfile
// would write a clinicSiteProfile aspect onto any live vertex the caller named.
// The guard asks the KEY, never the root class: a location vertex's class is
// its own key type, so a class check could not tell a building from a unit at
// all, and a `location` check would reject every location minted after the
// taxonomy landed.
//
// The negative vector below is a live UNIT — a real location, the wrong level —
// which is the case a weaker guard would let through. Its positive vector is
// TestClinic_SetSiteProfile above, over a real building.
func TestClinic_SetSiteProfileRejectsNonLocationBuilding(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "site-profile-badclass")

	notABuilding := "vtx.unit.CLnotabuiLdingHJKMNP"
	clSeedVertex(t, ctx, conn, notABuilding, "unit", false) // a real location, the WRONG level

	outcome, why := clSubmitSiteProfileReason(t, ctx, conn, cp, cons, "spbad0001", notABuilding)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("SetSiteProfile on a unit, not a building = %v, want rejected", outcome)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the building guard's own NotALocation", why)
	}
	if !clMissing(t, ctx, conn, notABuilding+".site") {
		t.Fatalf("a .site aspect was committed for a key that is not a building")
	}

	// The CLASS arm, which the key arm cannot stand in for: a building KEY
	// carrying a class location-domain never writes. `vtx.building.*` is
	// location-domain's keyspace but nothing stops another package minting
	// there, and the class is what proves who did.
	foreignClass := "vtx.building.CLforeignCLassHJKMNP"
	clSeedVertex(t, ctx, conn, foreignClass, "identity", false)
	outcome, why = clSubmitSiteProfileReason(t, ctx, conn, cp, cons, "spbad0002", foreignClass)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("SetSiteProfile on a building key of a foreign class = %v, want rejected", outcome)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the building guard's own NotALocation", why)
	}
	if !clMissing(t, ctx, conn, foreignClass+".site") {
		t.Fatalf("a .site aspect was committed for a building-keyed vertex of a foreign class")
	}

	// The retired migration widening stays retired: a building key carrying
	// the old shared pre-taxonomy class is refused like any other
	// wrong-classed key (dynamic-type-taxonomy-design.md §17.22 — the live
	// legacy-classed roots were rewritten to their key type 2026-08-10).
	legacy := "vtx.building.CLgacyCLassHJKMNPQRS"
	clSeedVertex(t, ctx, conn, legacy, "location", false)
	outcome, why = clSubmitSiteProfileReason(t, ctx, conn, cp, cons, "spbad0003", legacy)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("SetSiteProfile on a legacy-classed building = %v, want rejected", outcome)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the building guard's own NotALocation", why)
	}
	if !clMissing(t, ctx, conn, legacy+".site") {
		t.Fatalf("a .site aspect was committed for a legacy-classed building")
	}
}

func TestClinic_AssignProviderSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "assign-site")

	providerKey := createProvider(t, ctx, conn, cp, cons, "asprv0001", "Dr. Sam Okafor", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "asbldg0001")

	assignProviderSite(t, ctx, conn, cp, cons, "asassign0001", providerKey, buildingKey, processor.OutcomeAccepted)

	lk := practicesAtLinkKey(providerKey, buildingKey)
	doc := clReadDoc(t, ctx, conn, lk)
	if doc["class"] != "practicesAt" {
		t.Fatalf("link class = %v, want practicesAt", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("practicesAt link should be alive; got isDeleted=%v", del)
	}
	if sv, _ := doc["sourceVertex"].(string); sv != providerKey {
		t.Fatalf("link sourceVertex = %q, want %q (the provider)", sv, providerKey)
	}
	if tv, _ := doc["targetVertex"].(string); tv != buildingKey {
		t.Fatalf("link targetVertex = %q, want %q (the building)", tv, buildingKey)
	}
}

func TestClinic_AssignProviderSiteIdempotent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "assign-site-idem")

	providerKey := createProvider(t, ctx, conn, cp, cons, "aiprv0001", "Dr. Idem", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "aibldg0001")

	assignProviderSite(t, ctx, conn, cp, cons, "aiassign0001", providerKey, buildingKey, processor.OutcomeAccepted)
	// Second assign: already live -> idempotent no-op, still Accepted.
	assignProviderSite(t, ctx, conn, cp, cons, "aiassign0002", providerKey, buildingKey, processor.OutcomeAccepted)

	doc := clReadDoc(t, ctx, conn, practicesAtLinkKey(providerKey, buildingKey))
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("link should remain alive after a re-assign; got isDeleted=%v", del)
	}
}

func TestClinic_RemoveThenReassignProviderSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "remove-reassign-site")

	providerKey := createProvider(t, ctx, conn, cp, cons, "rrprv0001", "Dr. Reassign", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "rrbldg0001")
	lk := practicesAtLinkKey(providerKey, buildingKey)

	assignProviderSite(t, ctx, conn, cp, cons, "rrassign0001", providerKey, buildingKey, processor.OutcomeAccepted)

	removeProviderSite(t, ctx, conn, cp, cons, "rrremove0001", providerKey, buildingKey, processor.OutcomeAccepted)
	dead := clReadDoc(t, ctx, conn, lk)
	if del, _ := dead["isDeleted"].(bool); !del {
		t.Fatalf("link should be tombstoned after RemoveProviderSite; got isDeleted=%v", del)
	}

	// Re-assign revives the tombstoned link (a blind create would collide).
	assignProviderSite(t, ctx, conn, cp, cons, "rrassign0002", providerKey, buildingKey, processor.OutcomeAccepted)
	revived := clReadDoc(t, ctx, conn, lk)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("link should be alive again after re-assign (revive); got isDeleted=%v", del)
	}
}

func TestClinic_AssignProviderSiteRejectsDeadProvider(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "assign-site-dead")

	deadProvider := "vtx.provider.CLdeadprovHJKMNPQR"
	clSeedVertex(t, ctx, conn, deadProvider, "provider", true) // alive=false
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "adbldg0001")

	assignProviderSite(t, ctx, conn, cp, cons, "adassign0001", deadProvider, buildingKey, processor.OutcomeRejected)
	if !clMissing(t, ctx, conn, practicesAtLinkKey(deadProvider, buildingKey)) {
		t.Fatalf("a practicesAt link was committed for a dead provider")
	}
}

// clCreateAppointmentWithSite submits CreateAppointment with an optional site
// param (Increment 2) — the counterpart of integration_test.go's
// clCreateAppointmentWithLease. When siteKey is non-empty, both it and the
// provider→site practicesAt link are declared optionalReads
// (require_site_membership, ddls.go).
func clCreateAppointmentWithSite(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey, providerKey, siteKey string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{
		"patient": patientKey, "provider": providerKey,
		"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z",
	}
	optionalReads := []string{}
	if siteKey != "" {
		payloadMap["site"] = siteKey
		optionalReads = append(optionalReads, siteKey, practicesAtLinkKey(providerKey, siteKey))
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         clStaffActorKey,
		SubmittedAt:   clSubmittedAnchor,
		Class:         "appointment",
		Payload:       payload,
		ContextHint: &processor.ContextHint{Reads: []string{patientKey, providerKey}, OptionalReads: optionalReads,
			Enumerations: testutil.DeclaredEnumerations("CreateAppointment", clStaffActorKey, clinicdomain.OpMetas())},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return clNanoIDFromRequestID(reqID)
}

// TestClinic_CreateAppointment_WithValidSite proves a provider who
// practicesAt the given site books successfully and the appointment carries
// an atSite link (appointment→building).
func TestClinic_CreateAppointment_WithValidSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-with-site")

	patientKey := createPatient(t, ctx, conn, cp, cons, "apwsitepat01", "Sam Sitebooker")
	providerKey := createProvider(t, ctx, conn, cp, cons, "apwsiteprv01", "Dr. Site Ready", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "apwsitebld01")
	clSubmit(t, ctx, conn, cp, cons, "apwsiteset01", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Downtown Clinic"}`, []string{buildingKey}, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "apwsiteasg01", providerKey, buildingKey, processor.OutcomeAccepted)

	apptID := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "apwsiteappt1", patientKey, providerKey, buildingKey, processor.OutcomeAccepted)

	_, buildingID, _ := substrate.ParseVertexKey(buildingKey)
	atSiteLnk := "lnk.appointment." + apptID + ".atSite.building." + buildingID
	doc := clReadDoc(t, ctx, conn, atSiteLnk)
	if doc["class"] != "atSite" {
		t.Fatalf("atSite link class = %v, want atSite", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("atSite link should be alive; got isDeleted=%v", del)
	}
}

// TestClinic_CreateAppointment_RejectsProviderNotAtSite proves a site is
// hard-validated, unlike leaseAppKey's silent fall-through: a provider not
// assigned to the given site rejects the WHOLE booking (ProviderNotAtSite),
// committing no appointment at all.
func TestClinic_CreateAppointment_RejectsProviderNotAtSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-wrong-site")

	patientKey := createPatient(t, ctx, conn, cp, cons, "apwrongpat01", "Nora Notassigned")
	providerKey := createProvider(t, ctx, conn, cp, cons, "apwrongprv01", "Dr. No Site", "Cardiology")
	buildingKey := clCreateBuilding(t, ctx, conn, cp, cons, "apwrongbld01")
	clSubmit(t, ctx, conn, cp, cons, "apwrongset01", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+buildingKey+`","name":"Uptown Clinic"}`, []string{buildingKey}, processor.OutcomeAccepted)
	// Deliberately no AssignProviderSite — the provider does not practice here.

	apptID := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "apwrongappt1", patientKey, providerKey, buildingKey, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("no appointment should be committed when the provider is not assigned to the requested site")
	}
}

// TestClinic_CreateAppointment_RejectsNonLocationSite proves a site key whose
// KEY TYPE SEGMENT is not `building` is rejected (NotALocation), mirroring
// SetSiteProfile's own guard. The vector is a live UNIT — a real location at
// the wrong level — and its positive sibling is TestClinic_CreateAppointment's
// own site-bearing path over a real building.
func TestClinic_CreateAppointment_RejectsNonLocationSite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "appt-badclass-site")

	patientKey := createPatient(t, ctx, conn, cp, cons, "apbadpat0001", "Gail Ghostsite")
	providerKey := createProvider(t, ctx, conn, cp, cons, "apbadprv0001", "Dr. Bad Class", "Cardiology")
	fakeSite := "vtx.unit.CLapbadsiteHJKMNPQRS"
	clSeedVertex(t, ctx, conn, fakeSite, "unit", false) // a real location, the WRONG level

	apptID := clCreateAppointmentWithSite(t, ctx, conn, cp, cons, "apbadappt001", patientKey, providerKey, fakeSite, processor.OutcomeRejected)

	if !clMissing(t, ctx, conn, "vtx.appointment."+apptID) {
		t.Fatalf("no appointment should be committed for a non-location site")
	}
}

// TestClinic_ProviderMultipleSites proves a provider may practice at MANY
// sites: two AssignProviderSite calls against two different buildings both
// commit distinct, live links.
func TestClinic_ProviderMultipleSites(t *testing.T) {
	t.Parallel()
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "multi-site")

	providerKey := createProvider(t, ctx, conn, cp, cons, "msprv0001", "Dr. Multi", "Cardiology")
	buildingA := clCreateBuilding(t, ctx, conn, cp, cons, "msbldgA001")
	buildingB := clCreateBuilding(t, ctx, conn, cp, cons, "msbldgB001")

	assignProviderSite(t, ctx, conn, cp, cons, "msassignA001", providerKey, buildingA, processor.OutcomeAccepted)
	assignProviderSite(t, ctx, conn, cp, cons, "msassignB001", providerKey, buildingB, processor.OutcomeAccepted)

	for _, b := range []string{buildingA, buildingB} {
		doc := clReadDoc(t, ctx, conn, practicesAtLinkKey(providerKey, b))
		if del, _ := doc["isDeleted"].(bool); del {
			t.Fatalf("link to %s should be alive; got isDeleted=%v", b, del)
		}
	}
}
