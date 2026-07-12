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
//  2. TestClinic_SetSiteProfileRejectsNonLocationBuilding — wrong-class target rejected
//  3. TestClinic_AssignProviderSite           — practicesAt link committed alive, source=provider, target=building
//  4. TestClinic_AssignProviderSiteIdempotent — re-assign is a clean no-op
//  5. TestClinic_RemoveThenReassignProviderSite — remove tombstones; re-assign revives (CAS), alive again
//  6. TestClinic_AssignProviderSiteRejectsDeadProvider — tombstoned provider → Rejected, no link
//  7. TestClinic_ProviderMultipleSites         — one provider practicesAt two different buildings, both links alive
package clinicdomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
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

// TestClinic_SetSiteProfileRejectsNonLocationBuilding: a building-shaped key
// whose class is not location is rejected (NotALocation) and no aspect is
// committed.
func TestClinic_SetSiteProfileRejectsNonLocationBuilding(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	cp, cons := newClinicPipeline(t, ctx, conn, "site-profile-badclass")

	fakeBuilding := "vtx.building.CLnotlocatnHJKMNPQR"
	clSeedVertex(t, ctx, conn, fakeBuilding, "identity", false) // building-shaped key, wrong class

	clSubmit(t, ctx, conn, cp, cons, "spbad0001", "SetSiteProfile", "clinicSite",
		`{"buildingKey":"`+fakeBuilding+`","name":"Ghost Clinic"}`,
		[]string{fakeBuilding}, processor.OutcomeRejected)
	if !clMissing(t, ctx, conn, fakeBuilding+".site") {
		t.Fatalf("a .site aspect was committed for a non-location building")
	}
}

func TestClinic_AssignProviderSite(t *testing.T) {
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

// TestClinic_ProviderMultipleSites proves a provider may practice at MANY
// sites: two AssignProviderSite calls against two different buildings both
// commit distinct, live links.
func TestClinic_ProviderMultipleSites(t *testing.T) {
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
