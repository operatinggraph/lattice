// Listing / address integration tests for the loftspace-domain Capability
// Package.
//
// External test package (loftspacedomain_test) so the tests exercise the public
// Lattice surface a real package sees: seed the kernel, install rbac-domain +
// identity-domain + identity-hygiene + location-domain + loftspace-domain
// through the Processor, mint a location unit, then submit SetListing /
// SetUnitAddress and assert the committed Core-KV shape — the listing / address
// aspects land on the unit (class listing / address), optional fields included,
// and the unit-class + status guards reject bad input.
//
// Coverage:
//  1. TestLoftspace_SetListingAndAddress — listing + address aspects on a unit, optional fields
//  2. TestLoftspace_SetListingUpsert     — re-publish overwrites in place (status available→leased)
//  3. TestLoftspace_RejectsBadStatus     — status not in {available,pending,leased} → Rejected
//  4. TestLoftspace_RejectsNonUnit       — target alive but class≠location → Rejected (NotAUnit guard)
//  5. TestLoftspace_RejectsDeadUnit      — tombstoned unit → Rejected
//  6. TestLoftspace_UnauthorizedDenied   — consumer cap (no listing ops) → Rejected
package loftspacedomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	locationdomain "github.com/operatinggraph/lattice/packages/location-domain"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

const (
	lsStaffActorID   = "LSstaffActHJKMNPQRST"
	lsStaffActorKey  = "vtx.identity." + lsStaffActorID
	lsStaffCapKey    = "cap.identity." + lsStaffActorID
	lsConsumerID     = "LSconsumerHJKMNPQRST"
	lsConsumerKey    = "vtx.identity." + lsConsumerID
	lsConsumerCapKey = "cap.identity." + lsConsumerID
)

// loftspaceOps are the ops the staff actor needs: CreateLocation (to mint the
// unit it operates on) + the loftspace ops.
var loftspaceOps = []string{"CreateLocation", "SetListing", "SetUnitAddress", "SetListingStatus", "AssignUnitOwner", "RemoveUnitOwner"}

func lsStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	perms := make([]processor.PlatformPermission, 0, len(loftspaceOps))
	for _, op := range loftspaceOps {
		perms = append(perms, processor.PlatformPermission{OperationType: op, Scope: "any"})
	}
	return &processor.CapabilityDoc{
		Key:                    lsStaffCapKey,
		Actor:                  lsStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{lsStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

func lsConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    lsConsumerCapKey,
		Actor:                  lsConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{lsConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

// setupLoftspaceEnv seeds the kernel, installs the phase-1 packages +
// location-domain (the dependency) + loftspace-domain through the real
// meta-install pipeline, and seeds the cap docs.
func setupLoftspaceEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, locationdomain.Package); err != nil {
		stop()
		t.Fatalf("install location-domain: %v", err)
	}
	if _, err := inst.Install(ctx, loftspacedomain.Package); err != nil {
		stop()
		t.Fatalf("install loftspace-domain: %v", err)
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, lsStaffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, lsConsumerCapDoc())
	return ctx, conn
}

func newLoftspacePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ls-" + durable,
	})
}

func lsNanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func lsSeedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, isDeleted bool) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": isDeleted, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

func lsReadDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
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

// createUnit submits CreateLocation(unit) and returns the minted unit key.
func createUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer) string {
	t.Helper()
	reqID := testutil.GenReqID("mkunit")
	unitKey := "vtx.unit." + lsNanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         lsStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "location",
		Payload:       json.RawMessage(`{"locationType":"unit"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return unitKey
}

// setListing submits SetListing on the given unit with the given payload and the
// expected outcome.
func setListing(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, unitKey, payload string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetListing",
		Actor:         lsStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceListing",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// setListingStatus submits SetListingStatus on the given unit. class="" mirrors
// how Weaver's actuator dispatches a directOp (the operationType resolves via the
// empty-class permittedCommands reverse index) — the real convergence path; a
// manual operator call carries "loftspaceListing".
func setListingStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, unitKey, class, payload string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetListingStatus",
		Actor:         lsStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         class,
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey, unitKey + ".listing"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestLoftspace_SetListingStatus proves the status-only transition op: it flips
// .listing.status while PRESERVING the economics verbatim, rejects a unit with no
// listing (NoListing), is an idempotent no-op when already at the target status,
// and validates the status enum. The transition uses class="" to exercise the
// directOp dispatch path (the empty-class reverse index) — exactly how the
// leaseApplicationComplete convergence target drives it on approval.
func TestLoftspace_SetListingStatus(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "set-status")

	unitKey := createUnit(t, ctx, conn, cp, cons)

	// NoListing: a unit with no .listing yet is not transitionable, and the op
	// must NOT mint a bare {status}-only listing.
	setListingStatus(t, ctx, conn, cp, cons, "noList0001", unitKey, "loftspaceListing",
		`{"unit":"`+unitKey+`","status":"leased"}`, processor.OutcomeRejected)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, unitKey+".listing"); err == nil {
		t.Fatalf("SetListingStatus minted a listing on a unit that had none")
	}

	// Seed a full listing (available) with optional fields.
	setListing(t, ctx, conn, cp, cons, "seedList001", unitKey,
		`{"unit":"`+unitKey+`","rentAmount":2400,"rentCurrency":"USD","bedrooms":2,"bathrooms":1.5,"sqft":950,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`,
		processor.OutcomeAccepted)

	// Transition available → leased via the directOp path (empty class).
	setListingStatus(t, ctx, conn, cp, cons, "toLeased001", unitKey, "",
		`{"unit":"`+unitKey+`","status":"leased"}`, processor.OutcomeAccepted)

	ldoc := lsReadDoc(t, ctx, conn, unitKey+".listing")
	if ldoc["class"] != "listing" {
		t.Fatalf("listing class = %v, want listing (status flip must not change class)", ldoc["class"])
	}
	if vk, _ := ldoc["vertexKey"].(string); vk != unitKey {
		t.Fatalf("listing vertexKey = %q, want %q", vk, unitKey)
	}
	ldata, _ := ldoc["data"].(map[string]any)
	if ldata["status"] != "leased" {
		t.Fatalf("status = %v, want leased", ldata["status"])
	}
	// Economics preserved verbatim (the status-only rewrite must not drop fields).
	if ldata["rentCurrency"] != "USD" {
		t.Fatalf("rentCurrency not preserved: %v", ldata)
	}
	if ldata["availableFrom"] != "2026-08-01T00:00:00Z" {
		t.Fatalf("availableFrom not preserved: %v", ldata)
	}
	for _, f := range []string{"rentAmount", "bedrooms", "bathrooms", "sqft", "leaseTermMonths"} {
		if _, ok := ldata[f]; !ok {
			t.Fatalf("economics field %q dropped on status flip; data=%v", f, ldata)
		}
	}

	// Idempotent no-op: a re-dispatch to the SAME status (an at-least-once
	// duplicate) is ACCEPTED, leaves the listing leased, and writes NOTHING — the
	// KV revision must NOT bump (the no-op branch returns empty mutations, so no CDC
	// churn / no reprojection storm).
	beforeEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, unitKey+".listing")
	if err != nil {
		t.Fatalf("KVGet listing before no-op: %v", err)
	}
	setListingStatus(t, ctx, conn, cp, cons, "toLeased002", unitKey, "",
		`{"unit":"`+unitKey+`","status":"leased"}`, processor.OutcomeAccepted)
	afterEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, unitKey+".listing")
	if err != nil {
		t.Fatalf("KVGet listing after no-op: %v", err)
	}
	if afterEntry.Revision != beforeEntry.Revision {
		t.Fatalf("idempotent no-op re-dispatch bumped the listing revision %d → %d (it must write NOTHING)", beforeEntry.Revision, afterEntry.Revision)
	}
	if d, _ := lsReadDoc(t, ctx, conn, unitKey+".listing")["data"].(map[string]any); d["status"] != "leased" {
		t.Fatalf("idempotent re-dispatch changed status: %v", d)
	}

	// Bad status enum → rejected (economics untouched).
	setListingStatus(t, ctx, conn, cp, cons, "badStat001", unitKey, "loftspaceListing",
		`{"unit":"`+unitKey+`","status":"bogus"}`, processor.OutcomeRejected)
	if d, _ := lsReadDoc(t, ctx, conn, unitKey+".listing")["data"].(map[string]any); d["status"] != "leased" {
		t.Fatalf("a bad-status SetListingStatus mutated the listing: %v", d)
	}

	// Off-market: a landlord takes the unit off-market (withdrawn) and relists it
	// (available) — the same status-only transition, preserving the economics
	// verbatim. Proves the new enum value round-trips both ways.
	setListingStatus(t, ctx, conn, cp, cons, "toWithdrawn1", unitKey, "loftspaceListing",
		`{"unit":"`+unitKey+`","status":"withdrawn"}`, processor.OutcomeAccepted)
	if d, _ := lsReadDoc(t, ctx, conn, unitKey+".listing")["data"].(map[string]any); d["status"] != "withdrawn" {
		t.Fatalf("withdraw did not take: %v", d)
	} else if d["rentCurrency"] != "USD" || d["availableFrom"] != "2026-08-01T00:00:00Z" {
		t.Fatalf("withdraw dropped economics: %v", d)
	}
	setListingStatus(t, ctx, conn, cp, cons, "relist00001", unitKey, "loftspaceListing",
		`{"unit":"`+unitKey+`","status":"available"}`, processor.OutcomeAccepted)
	if d, _ := lsReadDoc(t, ctx, conn, unitKey+".listing")["data"].(map[string]any); d["status"] != "available" {
		t.Fatalf("relist did not restore available: %v", d)
	}
}

// TestLoftspace_SetListingStatusRejectsDeadUnit proves the alive guard on the
// status-only transition: a tombstoned unit cannot be transitioned.
func TestLoftspace_SetListingStatusRejectsDeadUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "status-dead")

	deadKey := "vtx.unit.LSdeadstatHJKMNPQR"
	lsSeedVertex(t, ctx, conn, deadKey, "location", true) // alive=false
	setListingStatus(t, ctx, conn, cp, cons, "deadStat001", deadKey, "loftspaceListing",
		`{"unit":"`+deadKey+`","status":"leased"}`, processor.OutcomeRejected)
}

// TestLoftspace_SetListingAndAddress mints a unit, sets a listing with optional
// fields and an address, and asserts both aspects land with the right class,
// vertexKey/localName envelope, and data.
func TestLoftspace_SetListingAndAddress(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "set")

	unitKey := createUnit(t, ctx, conn, cp, cons)

	setListing(t, ctx, conn, cp, cons, "setList0001", unitKey,
		`{"unit":"`+unitKey+`","rentAmount":2400,"rentCurrency":"USD","bedrooms":2,"bathrooms":1.5,"sqft":950,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`,
		processor.OutcomeAccepted)

	ldoc := lsReadDoc(t, ctx, conn, unitKey+".listing")
	if ldoc["class"] != "listing" {
		t.Fatalf("listing class = %v, want listing", ldoc["class"])
	}
	if vk, _ := ldoc["vertexKey"].(string); vk != unitKey {
		t.Fatalf("listing vertexKey = %q, want %q", vk, unitKey)
	}
	ldata, _ := ldoc["data"].(map[string]any)
	if ldata["status"] != "available" {
		t.Fatalf("listing status = %v, want available", ldata["status"])
	}
	if ldata["rentCurrency"] != "USD" {
		t.Fatalf("listing rentCurrency = %v, want USD", ldata["rentCurrency"])
	}
	// Optional fields landed.
	if _, ok := ldata["bathrooms"]; !ok {
		t.Fatalf("listing missing optional bathrooms; data=%v", ldata)
	}
	if _, ok := ldata["sqft"]; !ok {
		t.Fatalf("listing missing optional sqft; data=%v", ldata)
	}

	addrEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("setAddr0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetUnitAddress",
		Actor:         lsStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceListing",
		Payload:       json.RawMessage(`{"unit":"` + unitKey + `","line1":"123 Market St","city":"San Francisco","region":"CA","postal":"94103"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey}},
	}
	testutil.PublishOp(t, conn, addrEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	adoc := lsReadDoc(t, ctx, conn, unitKey+".address")
	if adoc["class"] != "address" {
		t.Fatalf("address class = %v, want address", adoc["class"])
	}
	adata, _ := adoc["data"].(map[string]any)
	if adata["city"] != "San Francisco" || adata["postal"] != "94103" {
		t.Fatalf("address data = %v, want city/postal set", adata)
	}
	// Optional line2 absent → not written.
	if _, ok := adata["line2"]; ok {
		t.Fatalf("address line2 should be absent when not supplied; data=%v", adata)
	}
}

// TestLoftspace_SetListingUpsert proves the unconditioned-upsert idiom: a second
// SetListing on the same unit overwrites the .listing aspect in place (one
// aspect, status flipped available→leased), not a conflict.
func TestLoftspace_SetListingUpsert(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "upsert")

	unitKey := createUnit(t, ctx, conn, cp, cons)
	base := `{"unit":"` + unitKey + `","rentAmount":2400,"rentCurrency":"USD","bedrooms":2,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,`

	setListing(t, ctx, conn, cp, cons, "upAvail0001", unitKey, base+`"status":"available"}`, processor.OutcomeAccepted)
	setListing(t, ctx, conn, cp, cons, "upLeased001", unitKey, base+`"status":"leased"}`, processor.OutcomeAccepted)

	ldoc := lsReadDoc(t, ctx, conn, unitKey+".listing")
	ldata, _ := ldoc["data"].(map[string]any)
	if ldata["status"] != "leased" {
		t.Fatalf("after re-publish, status = %v, want leased (overwrite-in-place)", ldata["status"])
	}
	if del, _ := ldoc["isDeleted"].(bool); del {
		t.Fatalf("listing should be alive after upsert; got isDeleted=%v", del)
	}
}

// TestLoftspace_RejectsBadStatus proves the status enum guard.
func TestLoftspace_RejectsBadStatus(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "bad-status")

	unitKey := createUnit(t, ctx, conn, cp, cons)
	setListing(t, ctx, conn, cp, cons, "badStat0001", unitKey,
		`{"unit":"`+unitKey+`","rentAmount":1,"rentCurrency":"USD","bedrooms":1,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"bogus"}`,
		processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, unitKey+".listing"); err == nil {
		t.Fatalf("a bad-status listing was committed on %s", unitKey)
	}
}

// TestLoftspace_RejectsNonUnit proves the class guard: a target that is alive
// and key-shaped as a unit (vtx.unit.<id>) but is NOT class=location is
// rejected — listing economics attach only to a real location unit.
func TestLoftspace_RejectsNonUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "non-unit")

	fakeKey := "vtx.unit.LSfakeunitHJKMNPQR"
	lsSeedVertex(t, ctx, conn, fakeKey, "identity", false) // unit-shaped key, wrong class

	setListing(t, ctx, conn, cp, cons, "nonUnit0001", fakeKey,
		`{"unit":"`+fakeKey+`","rentAmount":1,"rentCurrency":"USD","bedrooms":1,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`,
		processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, fakeKey+".listing"); err == nil {
		t.Fatalf("a listing was committed on a non-location vertex %s", fakeKey)
	}
}

// TestLoftspace_RejectsDeadUnit proves the alive guard: a tombstoned unit is
// rejected even though its key resolves.
func TestLoftspace_RejectsDeadUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "dead-unit")

	deadKey := "vtx.unit.LSdeadunitHJKMNPQR"
	lsSeedVertex(t, ctx, conn, deadKey, "location", true) // alive=false

	setListing(t, ctx, conn, cp, cons, "deadUnit001", deadKey,
		`{"unit":"`+deadKey+`","rentAmount":1,"rentCurrency":"USD","bedrooms":1,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`,
		processor.OutcomeRejected)
}

// TestLoftspace_UnauthorizedDenied submits SetListing as the consumer actor (no
// listing permissions). Expects OutcomeRejected.
func TestLoftspace_UnauthorizedDenied(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "unauth")

	unitKey := createUnit(t, ctx, conn, cp, cons)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("unauth0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetListing",
		Actor:         lsConsumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceListing",
		Payload:       json.RawMessage(`{"unit":"` + unitKey + `","rentAmount":1,"rentCurrency":"USD","bedrooms":1,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
