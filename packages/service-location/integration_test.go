// Service-location link-op integration tests for the service-location
// Capability Package.
//
// External test package (servicelocation_test) so the tests exercise the public
// Lattice surface a real package sees: seed the kernel, install rbac-domain +
// identity-domain + identity-hygiene + orchestration-base + location-domain +
// service-domain + service-location through the Processor, then submit the
// eight link ops and assert the committed Core-KV shape — each link is a
// sentence-valid topology edge whose endpoints are class-validated AT THE OP
// (residesIn target=location; availableAt/unavailableAt source=a service
// template, target=location; permitsOperation source=service, target=op-meta).
//
// Coverage:
//  1. TestSL_ResidesIn_WireUnwire          — identity→location link shape + direction + unwire
//  2. TestSL_AvailableAt_Wire              — service-template→location link (svc is source)
//  3. TestSL_UnavailableAt_Wire            — service-template→location exclusion link
//  4. TestSL_PermitsOperation_Wire         — service→op-meta link
//  5. TestSL_AvailableAt_RejectsInstance   — availableAt source must be a TEMPLATE (instance Rejected)
//  6. TestSL_ResidesIn_RejectsNonLocation  — residesIn target must be class=location
//  7. TestSL_PermitsOperation_RejectsNonOpMeta — permitsOperation target must carry operationType
//  8. TestSL_ResidesIn_Multiple            — residesIn cardinality is multiple
//  9. TestSL_UnauthorizedDenied            — consumer cap doc → Rejected
package servicelocation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	locationdomain "github.com/asolgan/lattice/packages/location-domain"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
	servicelocation "github.com/asolgan/lattice/packages/service-location"
)

const (
	slStaffActorID   = "SLstaffActHJKMNPQRST"
	slStaffActorKey  = "vtx.identity." + slStaffActorID
	slStaffCapKey    = "cap.identity." + slStaffActorID
	slConsumerID     = "SLconsumerHJKMNPQRST"
	slConsumerKey    = "vtx.identity." + slConsumerID
	slConsumerCapKey = "cap.identity." + slConsumerID
)

// slOps are the eight link ops the staff actor is granted (scope any).
var slOps = []string{
	"WireResidesIn", "UnwireResidesIn",
	"WireAvailableAt", "UnwireAvailableAt",
	"WireUnavailableAt", "UnwireUnavailableAt",
	"WirePermitsOperation", "UnwirePermitsOperation",
}

func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	perms := make([]processor.PlatformPermission, 0, len(slOps))
	for _, op := range slOps {
		perms = append(perms, processor.PlatformPermission{OperationType: op, Scope: "any"})
	}
	return &processor.CapabilityDoc{
		Key:                    slStaffCapKey,
		Actor:                  slStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{slStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{bootstrap.RoleOperatorKey},
	}
}

func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    slConsumerCapKey,
		Actor:                  slConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{slConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

// setupSLEnv seeds the kernel, installs the dependency chain (location-domain +
// service-domain) + service-location through the real meta-install pipeline,
// and seeds the cap docs.
func setupSLEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{
		locationdomain.Package,
		servicedomain.Package,
		servicelocation.Package,
	} {
		if _, err := inst.Install(ctx, pkg); err != nil {
			stop()
			t.Fatalf("install %s: %v", pkg.Name, err)
		}
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}

func newSLPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "sl-" + durable,
	})
}

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

// seedLocation / seedServiceTemplate / seedServiceInstance / seedOpMeta write
// the live endpoints directly into Core KV (the link ops only validate, they
// don't mint the endpoints).
func seedLocation(t *testing.T, ctx context.Context, conn *substrate.Conn, locType, id string) string {
	t.Helper()
	key := "vtx." + locType + "." + id
	seedVertex(t, ctx, conn, key, "location", nil)
	return key
}

func seedServiceTemplate(t *testing.T, ctx context.Context, conn *substrate.Conn, id, family string) string {
	t.Helper()
	key := "vtx.service." + id
	// P7: the template/instance discriminator is the vertex ENVELOPE class
	// (service.<family>.template), not a .class shadow aspect.
	seedVertex(t, ctx, conn, key, "service."+family+".template", nil)
	return key
}

func seedOpMeta(t *testing.T, ctx context.Context, conn *substrate.Conn, id, opType string) string {
	t.Helper()
	key := "vtx.meta." + id
	seedVertex(t, ctx, conn, key, "meta", map[string]any{"operationType": opType})
	return key
}

func submit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op string, payload map[string]any, reads []string, outcome processor.MessageOutcome) {
	t.Helper()
	pb, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         slStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "serviceLocation",
		Payload:       json.RawMessage(pb),
		ContextHint:   &processor.ContextHint{Reads: reads},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, outcome)
}

// TestSL_ResidesIn_WireUnwire wires identity→location and asserts the 6-segment
// residesIn link shape + direction (identity=source, location=target per
// Contract #1 §1.1 — "identity residesIn location"), then unwires it.
func TestSL_ResidesIn_WireUnwire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "residesin")

	idID := "SLresidentQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLunitresQQRHJKMNPQR"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)

	lnk := "lnk.identity." + idID + ".residesIn.unit." + unitID
	submit(t, ctx, conn, cp, cons, "slResWire1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		[]string{idKey, unitKey}, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if doc["class"] != "residesIn" {
		t.Fatalf("residesIn link class = %v, want residesIn", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != idKey {
		t.Fatalf("residesIn sourceVertex = %q, want %q (identity is source)", got, idKey)
	}
	if got, _ := doc["targetVertex"].(string); got != unitKey {
		t.Fatalf("residesIn targetVertex = %q, want %q (location is target)", got, unitKey)
	}

	submit(t, ctx, conn, cp, cons, "slResUnwire1", "UnwireResidesIn",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)
	doc = readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); !del {
		t.Fatalf("residesIn link should be tombstoned after unwire; got isDeleted=%v", del)
	}
}

// TestSL_AvailableAt_Wire wires a service-template→location availableAt link and
// asserts the direction (service is source — "service availableAt location").
func TestSL_AvailableAt_Wire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "availableat")

	tplID := "SLavaimtpmQRHJKMNPQR"
	tplKey := seedServiceTemplate(t, ctx, conn, tplID, "backgroundCheck")
	bldgID := "SLavaimbmdgRHJKMNPQR"
	bldgKey := seedLocation(t, ctx, conn, "building", bldgID)

	lnk := "lnk.service." + tplID + ".availableAt.building." + bldgID
	submit(t, ctx, conn, cp, cons, "slAvail1", "WireAvailableAt",
		map[string]any{"service": tplKey, "location": bldgKey},
		[]string{tplKey, bldgKey}, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if doc["class"] != "availableAt" {
		t.Fatalf("availableAt link class = %v, want availableAt", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != tplKey {
		t.Fatalf("availableAt sourceVertex = %q, want %q (service is source)", got, tplKey)
	}
	if got, _ := doc["targetVertex"].(string); got != bldgKey {
		t.Fatalf("availableAt targetVertex = %q, want %q (location is target)", got, bldgKey)
	}
}

// TestSL_UnavailableAt_Wire wires a service-template→location unavailableAt
// exclusion link.
func TestSL_UnavailableAt_Wire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "unavailableat")

	tplID := "SLunavtpmQQRHJKMNPQR"
	tplKey := seedServiceTemplate(t, ctx, conn, tplID, "backgroundCheck")
	penthID := "SLpenthousQRHJKMNPQR"
	penthKey := seedLocation(t, ctx, conn, "unit", penthID)

	lnk := "lnk.service." + tplID + ".unavailableAt.unit." + penthID
	submit(t, ctx, conn, cp, cons, "slUnav1", "WireUnavailableAt",
		map[string]any{"service": tplKey, "location": penthKey},
		[]string{tplKey, penthKey}, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if doc["class"] != "unavailableAt" {
		t.Fatalf("unavailableAt link class = %v, want unavailableAt", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != tplKey {
		t.Fatalf("unavailableAt sourceVertex = %q, want %q (service is source)", got, tplKey)
	}
}

// TestSL_PermitsOperation_Wire wires a service→op-meta permitsOperation link.
func TestSL_PermitsOperation_Wire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "permitsop")

	svcID := "SLpermsvcQQRHJKMNPQR"
	svcKey := seedServiceTemplate(t, ctx, conn, svcID, "backgroundCheck")
	opID := "SLopmetaQQQRHJKMNPQR"
	opKey := seedOpMeta(t, ctx, conn, opID, "BookLaundry")

	lnk := "lnk.service." + svcID + ".permitsOperation.meta." + opID
	submit(t, ctx, conn, cp, cons, "slPerm1", "WirePermitsOperation",
		map[string]any{"service": svcKey, "operation": opKey},
		[]string{svcKey, opKey}, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if doc["class"] != "permitsOperation" {
		t.Fatalf("permitsOperation link class = %v, want permitsOperation", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != svcKey {
		t.Fatalf("permitsOperation sourceVertex = %q, want %q (service is source)", got, svcKey)
	}
	if got, _ := doc["targetVertex"].(string); got != opKey {
		t.Fatalf("permitsOperation targetVertex = %q, want %q (op-meta is target)", got, opKey)
	}
}

// TestSL_AvailableAt_RejectsInstance proves the template guard at the op: an
// availableAt whose source is a service INSTANCE (envelope class ends .instance)
// is rejected — only templates carry availability assertions.
func TestSL_AvailableAt_RejectsInstance(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "availinst")

	instID := "SLinstanceQRHJKMNPQR"
	instKey := "vtx.service." + instID
	// P7: the instance carries its discriminator on the ENVELOPE class.
	seedVertex(t, ctx, conn, instKey, "service.backgroundCheck.instance", nil)
	bldgID := "SLinstbmdgQRHJKMNPQR"
	bldgKey := seedLocation(t, ctx, conn, "building", bldgID)

	submit(t, ctx, conn, cp, cons, "slAvailInst", "WireAvailableAt",
		map[string]any{"service": instKey, "location": bldgKey},
		[]string{instKey, bldgKey}, processor.OutcomeRejected)

	lnk := "lnk.service." + instID + ".availableAt.building." + bldgID
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lnk); err == nil {
		t.Fatalf("availableAt link from an instance was committed: %s", lnk)
	}
}

// TestSL_ResidesIn_RejectsNonLocation proves the location-class guard: a
// residesIn whose target is alive but NOT class=location is rejected.
func TestSL_ResidesIn_RejectsNonLocation(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resnonloc")

	idID := "SLresnonmocRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	// A unit-typed key whose class is NOT location — the rejection comes from
	// the class check, not the type segment.
	notLocID := "SLfakeunitQRHJKMNPQR"
	notLocKey := "vtx.unit." + notLocID
	seedVertex(t, ctx, conn, notLocKey, "service", nil)

	submit(t, ctx, conn, cp, cons, "slResNonLoc", "WireResidesIn",
		map[string]any{"identity": idKey, "location": notLocKey},
		[]string{idKey, notLocKey}, processor.OutcomeRejected)
}

// TestSL_PermitsOperation_RejectsNonOpMeta proves the op-meta guard: a
// permitsOperation whose target is a meta vertex with NO data.operationType is
// rejected.
func TestSL_PermitsOperation_RejectsNonOpMeta(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "permnonop")

	svcID := "SLpermnosvcRHJKMNPQR"
	svcKey := seedServiceTemplate(t, ctx, conn, svcID, "backgroundCheck")
	// A meta vertex with no operationType (a lens meta, say).
	badOpID := "SLnotanopQQRHJKMNPQR"
	badOpKey := "vtx.meta." + badOpID
	seedVertex(t, ctx, conn, badOpKey, "meta", map[string]any{"canonicalName": "someLens"})

	submit(t, ctx, conn, cp, cons, "slPermNoOp", "WirePermitsOperation",
		map[string]any{"service": svcKey, "operation": badOpKey},
		[]string{svcKey, badOpKey}, processor.OutcomeRejected)
}

// TestSL_ResidesIn_Multiple proves residesIn cardinality is multiple: an
// identity may reside in two distinct locations concurrently.
func TestSL_ResidesIn_Multiple(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resmulti")

	idID := "SLmumtiresQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitAKey := seedLocation(t, ctx, conn, "unit", "SLmumtiuaQRHJKMNPQRS")
	unitBKey := seedLocation(t, ctx, conn, "unit", "SLmumtiubQRHJKMNPQRS")

	submit(t, ctx, conn, cp, cons, "slMultiA", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitAKey},
		[]string{idKey, unitAKey}, processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slMultiB", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitBKey},
		[]string{idKey, unitBKey}, processor.OutcomeAccepted)

	for _, loc := range []string{unitAKey, unitBKey} {
		locID := strings.TrimPrefix(loc, "vtx.unit.")
		lnk := "lnk.identity." + idID + ".residesIn.unit." + locID
		doc := readDoc(t, ctx, conn, lnk)
		if del, _ := doc["isDeleted"].(bool); del {
			t.Fatalf("residesIn link %s should be alive (multiple residence allowed)", lnk)
		}
	}
}

// TestSL_UnauthorizedDenied submits WireResidesIn as the consumer actor (no
// scheme permissions). Expects OutcomeRejected.
func TestSL_UnauthorizedDenied(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "unauth")

	idKey := slConsumerKey
	unitKey := seedLocation(t, ctx, conn, "unit", "SLunauthmocRHJKMNPQR")
	pb, _ := json.Marshal(map[string]any{"identity": idKey, "location": unitKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("slUnauth01"),
		Lane:          processor.LaneDefault,
		OperationType: "WireResidesIn",
		Actor:         slConsumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "serviceLocation",
		Payload:       json.RawMessage(pb),
		ContextHint:   &processor.ContextHint{Reads: []string{idKey, unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
