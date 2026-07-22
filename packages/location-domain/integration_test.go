// Location lifecycle + containment integration tests for the location-domain
// Capability Package.
//
// External test package (locationdomain_test) so the tests exercise the public
// Lattice surface a real package sees: seed the kernel, install rbac-domain +
// identity-domain + identity-hygiene + location-domain through the Processor,
// then submit the four ops and assert the committed Core-KV shape — a location
// is a real class=location vertex with root data {} (D5), and containedIn is a
// child→parent link whose endpoints are validated alive + location-class.
//
// Coverage:
//  1. TestLocation_CreateAllTypes        — vtx.unit/building/property, class=location, root {}
//  2. TestLocation_ContainedInChain      — unit→building→property link key shape + direction
//  3. TestLocation_WireRejectsNonLocation — containedIn to a non-location vertex → Rejected
//  4. TestLocation_WireRejectsDeadEndpoint — containedIn to a tombstoned location → Rejected
//  5. TestLocation_UnwireContainedIn     — link tombstone
//  6. TestLocation_TombstoneLocation     — vertex tombstone
//  7. TestLocation_UnauthorizedDenied    — consumer cap doc → Rejected
package locationdomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	locationdomain "github.com/operatinggraph/lattice/packages/location-domain"
)

const (
	locStaffActorID   = "LDstaffActHJKMNPQRST"
	locStaffActorKey  = "vtx.identity." + locStaffActorID
	locStaffCapKey    = "cap.identity." + locStaffActorID
	locConsumerID     = "LDconsumerHJKMNPQRST"
	locConsumerKey    = "vtx.identity." + locConsumerID
	locConsumerCapKey = "cap.identity." + locConsumerID
)

// locationOps are the ops the staff actor is granted (scope any).
var locationOps = []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn", "SetLocationPresentation"}

// staffCapDoc grants the staff actor all four location ops (scope any).
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	perms := make([]processor.PlatformPermission, 0, len(locationOps))
	for _, op := range locationOps {
		perms = append(perms, processor.PlatformPermission{OperationType: op, Scope: "any"})
	}
	return &processor.CapabilityDoc{
		Key:                    locStaffCapKey,
		Actor:                  locStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{locStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc grants the consumer actor no location ops.
func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    locConsumerCapKey,
		Actor:                  locConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{locConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

// setupLocationEnv seeds the kernel, installs the phase-1 packages +
// location-domain through the real meta-install pipeline, and seeds the cap docs.
func setupLocationEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, locationdomain.Package); err != nil {
		stop()
		t.Fatalf("install location-domain: %v", err)
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}

func newLocationPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "loc-" + durable,
	})
}

// nanoIDFromRequestID predicts the NanoID the DDL's first nanoid.new() mints
// (deterministic from the requestId — the same algorithm the Processor uses).
func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// seedVertex writes a minimal live vertex directly to Core KV.
func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

// seedDeletedVertex writes a soft-deleted (isDeleted=true) location directly to
// Core KV: the key resolves (it is present + hydratable) but vertex_alive treats
// it as dead, so a containedIn endpoint check rejects it.
func seedDeletedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed deleted vertex %s: %v", key, err)
	}
}

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

// createLocation submits CreateLocation for the given type and returns the
// minted location key (predicted from the requestId).
func createLocation(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, locType string) string {
	t.Helper()
	reqID := testutil.GenReqID("mk" + locType)
	locID := nanoIDFromRequestID(reqID)
	locKey := "vtx." + locType + "." + locID
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationType":"` + locType + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return locKey
}

// TestLocation_CreateAllTypes mints a unit, a building, and a property; asserts
// each is a class=location vertex with root data minimal ({}, D5) under the
// vtx.<locationType>.<NanoID> key shape (Contract #6 §6.9).
func TestLocation_CreateAllTypes(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "create-types")

	for _, locType := range []string{"unit", "building", "property"} {
		locKey := createLocation(t, ctx, conn, cp, cons, locType)
		if !strings.HasPrefix(locKey, "vtx."+locType+".") {
			t.Fatalf("%s key = %q, want vtx.%s.<id>", locType, locKey, locType)
		}
		doc := readDoc(t, ctx, conn, locKey)
		if doc["class"] != "location" {
			t.Fatalf("%s class = %v, want location", locType, doc["class"])
		}
		if del, _ := doc["isDeleted"].(bool); del {
			t.Fatalf("%s should be alive", locType)
		}
		data, _ := doc["data"].(map[string]any)
		if len(data) != 0 {
			t.Fatalf("%s root data = %v, want {} (D5)", locType, data)
		}
	}
}

// keyExists reports whether a Core-KV key resolves (used to assert the absence
// of a .presentation aspect for an undescribed location).
func keyExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key); err != nil {
		return false
	}
	return true
}

// TestLocation_CreatePresentation proves CreateLocation's optional display name
// (display-name-convention-design.md class 2): a supplied presentation writes a
// .presentation aspect {name, icon} on the location, and an omitted presentation
// writes no aspect at all (an undescribed location degrades to a typed fallback,
// it is not "Unnamed").
func TestLocation_CreatePresentation(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "create-pres")

	// Named building.
	reqID := testutil.GenReqID("mkNamedBldg")
	locKey := "vtx.building." + nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationType":"building","presentation":{"name":"Riverside Building","icon":"building"}}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	presDoc := readDoc(t, ctx, conn, locKey+".presentation")
	if presDoc["class"] != "presentation" {
		t.Fatalf("presentation aspect class = %v, want presentation", presDoc["class"])
	}
	if vk, _ := presDoc["vertexKey"].(string); vk != locKey {
		t.Fatalf("presentation aspect vertexKey = %q, want %q", vk, locKey)
	}
	data, _ := presDoc["data"].(map[string]any)
	if got, _ := data["name"].(string); got != "Riverside Building" {
		t.Fatalf("presentation.name = %q, want %q", got, "Riverside Building")
	}
	if got, _ := data["icon"].(string); got != "building" {
		t.Fatalf("presentation.icon = %q, want %q", got, "building")
	}

	// An unnamed unit carries NO presentation aspect (degrade gracefully).
	bareKey := createLocation(t, ctx, conn, cp, cons, "unit")
	if keyExists(t, ctx, conn, bareKey+".presentation") {
		t.Fatalf("a location created without a presentation payload must carry no .presentation aspect")
	}
}

// TestLocation_SetLocationPresentation proves the live-world editor: setting a
// presentation on an existing location writes the aspect; an empty presentation
// object and a dead/absent subject are rejected.
func TestLocation_SetLocationPresentation(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "set-pres")

	unitKey := createLocation(t, ctx, conn, cp, cons, "unit")

	set := func(label, payload string, outcome processor.MessageOutcome) {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "SetLocationPresentation",
			Actor:         locStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "location",
			Payload:       json.RawMessage(payload),
			ContextHint:   &processor.ContextHint{Reads: []string{unitKey}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, outcome)
	}

	set("setPres00001", `{"locationKey":"`+unitKey+`","presentation":{"name":"Unit 1"}}`, processor.OutcomeAccepted)
	data, _ := readDoc(t, ctx, conn, unitKey+".presentation")["data"].(map[string]any)
	if got, _ := data["name"].(string); got != "Unit 1" {
		t.Fatalf("presentation.name = %q, want %q", got, "Unit 1")
	}

	// Replacing an existing presentation is a full-replace upsert, not a
	// create: the second set must land against an aspect that already carries
	// a revision, and must replace rather than merge (the dropped icon stays
	// dropped).
	set("setPres00002", `{"locationKey":"`+unitKey+`","presentation":{"name":"Unit 1A","description":"corner"}}`, processor.OutcomeAccepted)
	data, _ = readDoc(t, ctx, conn, unitKey+".presentation")["data"].(map[string]any)
	if got, _ := data["name"].(string); got != "Unit 1A" {
		t.Fatalf("replaced presentation.name = %q, want %q", got, "Unit 1A")
	}
	if got, _ := data["description"].(string); got != "corner" {
		t.Fatalf("replaced presentation.description = %q, want %q", got, "corner")
	}

	// An empty presentation object is rejected (nothing to set).
	set("setPresEmpty", `{"locationKey":"`+unitKey+`","presentation":{}}`, processor.OutcomeRejected)

	// A dead subject is rejected (endpoint-class/alive guard).
	deadID := "LDdeadunitHJKMNPQRST"
	deadKey := "vtx.unit." + deadID
	seedDeletedVertex(t, ctx, conn, deadKey, "location")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("setPresDead1"),
		Lane:          processor.LaneDefault,
		OperationType: "SetLocationPresentation",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationKey":"` + deadKey + `","presentation":{"name":"Ghost"}}`),
		ContextHint:   &processor.ContextHint{Reads: []string{deadKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLocation_ContainedInChain wires unit→building→property and asserts each
// containedIn link's 6-segment key shape + direction (child=source, parent=target
// per Contract #1 §1.1 — the sentence reads "unit containedIn building").
func TestLocation_ContainedInChain(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "chain")

	unitKey := createLocation(t, ctx, conn, cp, cons, "unit")
	buildingKey := createLocation(t, ctx, conn, cp, cons, "building")
	propertyKey := createLocation(t, ctx, conn, cp, cons, "property")

	unitID := strings.TrimPrefix(unitKey, "vtx.unit.")
	buildingID := strings.TrimPrefix(buildingKey, "vtx.building.")
	propertyID := strings.TrimPrefix(propertyKey, "vtx.property.")

	wire := func(label, child, parent string) {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "WireContainedIn",
			Actor:         locStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "location",
			Payload:       json.RawMessage(`{"child":"` + child + `","parent":"` + parent + `"}`),
			ContextHint:   &processor.ContextHint{Reads: []string{child, parent}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	}

	wire("wireUnitBldg", unitKey, buildingKey)
	wire("wireBldgProp", buildingKey, propertyKey)

	// unit containedIn building.
	unitLnk := "lnk.unit." + unitID + ".containedIn.building." + buildingID
	udoc := readDoc(t, ctx, conn, unitLnk)
	if udoc["class"] != "containedIn" {
		t.Fatalf("unit link class = %v, want containedIn", udoc["class"])
	}
	if got, _ := udoc["sourceVertex"].(string); got != unitKey {
		t.Fatalf("unit link sourceVertex = %q, want %q (child is source)", got, unitKey)
	}
	if got, _ := udoc["targetVertex"].(string); got != buildingKey {
		t.Fatalf("unit link targetVertex = %q, want %q (parent is target)", got, buildingKey)
	}

	// building containedIn property.
	bldgLnk := "lnk.building." + buildingID + ".containedIn.property." + propertyID
	bdoc := readDoc(t, ctx, conn, bldgLnk)
	if got, _ := bdoc["sourceVertex"].(string); got != buildingKey {
		t.Fatalf("building link sourceVertex = %q, want %q (child is source)", got, buildingKey)
	}
	if got, _ := bdoc["targetVertex"].(string); got != propertyKey {
		t.Fatalf("building link targetVertex = %q, want %q (parent is target)", got, propertyKey)
	}
}

// TestLocation_WireRejectsNonLocation proves the endpoint-class guard: wiring
// containedIn to a vertex that exists + is alive but is NOT class=location (an
// identity here) is rejected — the link is never committed into the place graph.
func TestLocation_WireRejectsNonLocation(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "non-location")

	unitKey := createLocation(t, ctx, conn, cp, cons, "unit")

	// A live identity vertex masquerading as a containment parent — same key
	// arity as a location, wrong class. Use a unit-typed key so location_parts
	// passes and the rejection comes from the CLASS check, not the type segment.
	notLocID := "LDfakeparentHJKMNPQR"
	notLocKey := "vtx.unit." + notLocID
	seedVertex(t, ctx, conn, notLocKey, "identity", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wireNonLoc01"),
		Lane:          processor.LaneDefault,
		OperationType: "WireContainedIn",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"child":"` + unitKey + `","parent":"` + notLocKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey, notLocKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// No containedIn link should exist.
	unitID := strings.TrimPrefix(unitKey, "vtx.unit.")
	lnk := "lnk.unit." + unitID + ".containedIn.unit." + notLocID
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lnk); err == nil {
		t.Fatalf("containedIn link to a non-location vertex was committed: %s", lnk)
	}
}

// TestLocation_WireRejectsDeadEndpoint proves the alive guard: wiring
// containedIn to a tombstoned (isDeleted) location is rejected even though the
// key resolves.
func TestLocation_WireRejectsDeadEndpoint(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "dead-endpoint")

	unitKey := createLocation(t, ctx, conn, cp, cons, "unit")

	deadBldgID := "LDdeadbuildHJKMNPQRS"
	deadBldgKey := "vtx.building." + deadBldgID
	seedDeletedVertex(t, ctx, conn, deadBldgKey, "location")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wireDead0001"),
		Lane:          processor.LaneDefault,
		OperationType: "WireContainedIn",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"child":"` + unitKey + `","parent":"` + deadBldgKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey, deadBldgKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLocation_UnwireContainedIn wires then unwires; asserts the link is
// tombstoned (isDeleted=true).
func TestLocation_UnwireContainedIn(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "unwire")

	unitKey := createLocation(t, ctx, conn, cp, cons, "unit")
	buildingKey := createLocation(t, ctx, conn, cp, cons, "building")
	unitID := strings.TrimPrefix(unitKey, "vtx.unit.")
	buildingID := strings.TrimPrefix(buildingKey, "vtx.building.")
	lnk := "lnk.unit." + unitID + ".containedIn.building." + buildingID

	wireEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("uwWire00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WireContainedIn",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"child":"` + unitKey + `","parent":"` + buildingKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey, buildingKey}},
	}
	testutil.PublishOp(t, conn, wireEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	unwireEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("uwUnwire001"),
		Lane:          processor.LaneDefault,
		OperationType: "UnwireContainedIn",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"linkKey":"` + lnk + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{lnk}},
	}
	testutil.PublishOp(t, conn, unwireEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); !del {
		t.Fatalf("containedIn link should be tombstoned after UnwireContainedIn; got isDeleted=%v", del)
	}
}

// TestLocation_TombstoneLocation creates then tombstones a location; asserts
// the vertex is soft-deleted.
func TestLocation_TombstoneLocation(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "tombstone")

	bldgKey := createLocation(t, ctx, conn, cp, cons, "building")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("tomb000001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneLocation",
		Actor:         locStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationKey":"` + bldgKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{bldgKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, bldgKey)
	if del, _ := doc["isDeleted"].(bool); !del {
		t.Fatalf("location should be tombstoned; got isDeleted=%v", del)
	}
}

// TestLocation_UnauthorizedDenied submits CreateLocation as the consumer actor
// (no location permissions). Expects OutcomeRejected.
func TestLocation_UnauthorizedDenied(t *testing.T) {
	ctx, conn := setupLocationEnv(t)
	cp, cons := newLocationPipeline(t, ctx, conn, "unauth")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("unauth0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         locConsumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationType":"unit"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
