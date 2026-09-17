// Service-location link-op integration tests for the service-location
// Capability Package.
//
// External test package (servicelocation_test) so the tests exercise the public
// Lattice surface a real package sees: seed the kernel, install rbac-domain +
// identity-domain + identity-hygiene + orchestration-base + location-domain +
// service-domain + service-location through the Processor, then submit the
// ten link ops and assert the committed Core-KV shape — each link is a
// sentence-valid topology edge whose endpoints are class-validated AT THE OP
// (residesIn / worksAt target=location; availableAt/unavailableAt source=a
// service template, target=location; permitsOperation source=service,
// target=op-meta).
//
// Coverage:
//  1. TestSL_ResidesIn_WireUnwire          — identity→location link shape + direction + unwire
//  2. TestSL_AvailableAt_Wire              — service-template→location link (svc is source)
//  3. TestSL_UnavailableAt_Wire            — service-template→location exclusion link
//  4. TestSL_PermitsOperation_Wire         — service→op-meta link
//  5. TestSL_AvailableAt_RejectsInstance   — availableAt source must be a TEMPLATE (instance Rejected)
//  6. TestSL_ResidesIn_RejectsNonLocation  — residesIn target must be a location KEY TYPE
//     TestSL_ResidesIn_RejectsForeignClassOnLocationKey — …and a location CLASS
//  7. TestSL_PermitsOperation_RejectsNonOpMeta — permitsOperation target must carry operationType
//  8. TestSL_ResidesIn_Multiple            — residesIn cardinality is multiple
//  9. TestSL_UnauthorizedDenied            — consumer cap doc → Rejected
//  10. TestSL_WorksAt_WireUnwire           — staff spine link shape + direction + unwire
//  11. TestSL_WorksAt_RejectsNonLocation   — worksAt target must be a location KEY TYPE
//  12. TestSL_WorksAt_Multiple             — worksAt cardinality is multiple
//  13. TestSL_ResidesIn_Rewire             — declared re-wire revives a tombstoned residesIn
//     TestSL_ResidesIn_RewireUndeclared   — …and so does one that never declared the link key
//     TestSL_ResidesIn_UndeclaredAliveIsNoOp / TestSL_ResidesIn_UndeclaredFirstWireCreates
//     — the undeclared walk's other two arms
//     TestSL_ResidesIn_RewireUndeclaredPastFirstPage — the walk pages
//     TestSL_ResidesIn_PastBoundAbsentCreates / TestSL_ResidesIn_PastBoundTombstoneConflicts
//     — past the walk's bound the wire is a create-once create
package servicelocation_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
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
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
	servicelocation "github.com/operatinggraph/lattice/packages/service-location"
)

const (
	slStaffActorID   = "SLstaffActHJKMNPQRST"
	slStaffActorKey  = "vtx.identity." + slStaffActorID
	slStaffCapKey    = "cap.identity." + slStaffActorID
	slConsumerID     = "SLconsumerHJKMNPQRST"
	slConsumerKey    = "vtx.identity." + slConsumerID
	slConsumerCapKey = "cap.identity." + slConsumerID
)

// slOps are the ten link ops the staff actor is granted (scope any).
var slOps = []string{
	"WireResidesIn", "UnwireResidesIn",
	"WireWorksAt", "UnwireWorksAt",
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
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
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
	seedVertex(t, ctx, conn, key, locType, nil)
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

// linkKeyOf builds the deterministic 6-segment link key for "source <relation>
// target" from the two vtx.<type>.<id> endpoint keys (Contract #1 §1.1).
func linkKeyOf(source, relation, target string) string {
	return "lnk." + strings.TrimPrefix(source, "vtx.") + "." + relation + "." + strings.TrimPrefix(target, "vtx.")
}

// wireHint is the read declaration every Wire* op requires (ddls.go): both
// endpoints fail-closed, plus the deterministic link key as an OPTIONAL read.
// It is optional because a first wire legitimately finds it absent. For
// worksAt / availableAt / unavailableAt / permitsOperation it is what lets the
// script tell a tombstoned link from an absent one, so a re-wire after an
// Unwire* without it emits a create over a live key. residesIn also declares
// the walk the script runs whenever its snapshot lacks the link — the
// identity's own residesIn page — so the pipeline's read-drift guard sees it
// declared exactly as the Weaver's dispatch declares it.
func wireHint(source, relation, target string) *processor.ContextHint {
	h := &processor.ContextHint{
		Reads:         []string{source, target},
		OptionalReads: []string{linkKeyOf(source, relation, target)},
	}
	if relation == "residesIn" {
		h.Enumerations = residesInEnumeration(source)
	}
	return h
}

// residesInEnumeration is the contextHint.enumerations entry a WireResidesIn
// dispatcher declares: the identity's own residesIn links, walked outward.
func residesInEnumeration(identity string) []processor.EnumerationHint {
	return []processor.EnumerationHint{{Hub: identity, Relation: "residesIn", Direction: "out"}}
}

func submit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op string, payload map[string]any, reads []string, outcome processor.MessageOutcome) {
	t.Helper()
	submitHint(t, ctx, conn, cp, cons, label, op, payload, &processor.ContextHint{Reads: reads}, outcome)
}

func submitHint(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op string, payload map[string]any, hint *processor.ContextHint, outcome processor.MessageOutcome) {
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
		ContextHint:   hint,
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, outcome)
}

// submitHintWithReason is submitHint's sibling for a REJECTION vector: it
// returns the script's own failure message so a test can name the guard it
// means to exercise. A bare outcome check cannot tell the location guard from
// a hydration miss, an auth denial, or a malformed key.
func submitHintWithReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, op string, payload map[string]any, hint *processor.ContextHint) (processor.MessageOutcome, string) {
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
		ContextHint:   hint,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
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
	submitHint(t, ctx, conn, cp, cons, "slResWire1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)

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

// TestSL_WorksAt_WireUnwire wires the staff spine identity→location and asserts
// the 6-segment worksAt link shape + direction (identity=source, location=target
// per Contract #1 §1.1 — "identity worksAt location"), then unwires it.
func TestSL_WorksAt_WireUnwire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "worksat")

	idID := "SLstaffQRHJKMNPQRSTU"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	bldgID := "SLworkbdgRHJKMNPQRST"
	bldgKey := seedLocation(t, ctx, conn, "building", bldgID)

	lnk := "lnk.identity." + idID + ".worksAt.building." + bldgID
	submitHint(t, ctx, conn, cp, cons, "slWorkWire1", "WireWorksAt",
		map[string]any{"identity": idKey, "location": bldgKey},
		wireHint(idKey, "worksAt", bldgKey), processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, lnk)
	if doc["class"] != "worksAt" {
		t.Fatalf("worksAt link class = %v, want worksAt", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != idKey {
		t.Fatalf("worksAt sourceVertex = %q, want %q (identity is source)", got, idKey)
	}
	if got, _ := doc["targetVertex"].(string); got != bldgKey {
		t.Fatalf("worksAt targetVertex = %q, want %q (location is target)", got, bldgKey)
	}

	submit(t, ctx, conn, cp, cons, "slWorkUnwire1", "UnwireWorksAt",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)
	doc = readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); !del {
		t.Fatalf("worksAt link should be tombstoned after unwire; got isDeleted=%v", del)
	}
}

// TestSL_ResidesIn_Rewire proves a tombstoned residesIn link can be REVIVED:
// wire → unwire → re-wire the same endpoints. The re-wire must find the
// tombstone (the link key is an optional read) and emit an update; a create
// would assert revision 0 against a key already at a later revision and fail
// RevisionConflict, which is what stranded a resident who moved out — they
// could never be moved back into the same unit.
func TestSL_ResidesIn_Rewire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resrewire")

	idID := "SLremoveinQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLrewireunQRHJKMNPQR"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)

	lnk := "lnk.identity." + idID + ".residesIn.unit." + unitID
	submitHint(t, ctx, conn, cp, cons, "slResRewire1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slResRewire2", "UnwireResidesIn",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)
	submitHint(t, ctx, conn, cp, cons, "slResRewire3", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)

	assertLinkRevived(t, ctx, conn, lnk, "residesIn", idKey, unitKey)
}

// TestSL_WorksAt_Rewire is the worksAt half of the same revive vector — the
// case that stranded the showcase staff persona: their workplace link was
// unwired while testing and no WireWorksAt could re-establish it.
func TestSL_WorksAt_Rewire(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "workrewire")

	idID := "SLrewirestQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	bldgID := "SLrewirebdQRHJKMNPQR"
	bldgKey := seedLocation(t, ctx, conn, "building", bldgID)

	lnk := "lnk.identity." + idID + ".worksAt.building." + bldgID
	submitHint(t, ctx, conn, cp, cons, "slWorkRewire1", "WireWorksAt",
		map[string]any{"identity": idKey, "location": bldgKey},
		wireHint(idKey, "worksAt", bldgKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slWorkRewire2", "UnwireWorksAt",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)
	submitHint(t, ctx, conn, cp, cons, "slWorkRewire3", "WireWorksAt",
		map[string]any{"identity": idKey, "location": bldgKey},
		wireHint(idKey, "worksAt", bldgKey), processor.OutcomeAccepted)

	assertLinkRevived(t, ctx, conn, lnk, "worksAt", idKey, bldgKey)
}

// assertLinkRevived asserts a re-wired link is alive again and still carries
// its full body — the revive writes the whole document, so a partial one would
// leave the lens walking an edge with no endpoints.
func assertLinkRevived(t *testing.T, ctx context.Context, conn *substrate.Conn, lnk, class, source, target string) {
	t.Helper()
	doc := readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("%s link should be alive after re-wire; got isDeleted=true", class)
	}
	if doc["class"] != class {
		t.Fatalf("re-wired link class = %v, want %s", doc["class"], class)
	}
	if got, _ := doc["sourceVertex"].(string); got != source {
		t.Fatalf("re-wired sourceVertex = %q, want %q", got, source)
	}
	if got, _ := doc["targetVertex"].(string); got != target {
		t.Fatalf("re-wired targetVertex = %q, want %q", got, target)
	}
}

// undeclaredWireHint is the read declaration a convergence dispatcher sends
// WireResidesIn: both endpoints and the residesIn walk, and NO link key — the
// Weaver composes no link keys, so the script's snapshot never carries the
// link and every state of it has to come from the script's own residesIn
// page.
func undeclaredWireHint(source, target string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:        []string{source, target},
		Enumerations: residesInEnumeration(source),
	}
}

// submitWireResidesInReply submits WireResidesIn under hint and returns the
// outcome plus the reply, so a test can read the primaryKey the script
// surfaced (or its absence on an idempotent no-op).
func submitWireResidesInReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, idKey, unitKey string, hint *processor.ContextHint) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	pb, _ := json.Marshal(map[string]any{"identity": idKey, "location": unitKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "WireResidesIn",
		Actor:         slStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "serviceLocation",
		Payload:       json.RawMessage(pb),
		ContextHint:   hint,
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// TestSL_ResidesIn_RewireUndeclared proves WireResidesIn revives a tombstoned
// residesIn link for a submitter that did NOT list the link key in
// contextHint.optionalReads — the convergence dispatch that re-wires a
// residence a lease's end unwired. The snapshot reads the key as absent, so
// the script lists the identity's own residesIn page, finds the tombstone
// there and revives it pinned to the page entry's revision. The revived
// document is compared field-for-field with the one a DECLARED revive writes
// for the same identity: the two paths differ only in where the tombstone was
// read from, never in what they write.
func TestSL_ResidesIn_RewireUndeclared(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resrewireundecl")

	idID := "SLundecidnQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	declUnitID := "SLundecdecQRHJKMNPQR"
	declUnitKey := seedLocation(t, ctx, conn, "unit", declUnitID)
	undeclUnitID := "SLundecundQRHJKMNPQR"
	undeclUnitKey := seedLocation(t, ctx, conn, "unit", undeclUnitID)

	// The reference: wire → unwire → declared re-wire on the first unit.
	declLnk := linkKeyOf(idKey, "residesIn", declUnitKey)
	submitHint(t, ctx, conn, cp, cons, "slUndeclRef1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": declUnitKey},
		wireHint(idKey, "residesIn", declUnitKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slUndeclRef2", "UnwireResidesIn",
		map[string]any{"linkKey": declLnk}, []string{declLnk}, processor.OutcomeAccepted)
	submitHint(t, ctx, conn, cp, cons, "slUndeclRef3", "WireResidesIn",
		map[string]any{"identity": idKey, "location": declUnitKey},
		wireHint(idKey, "residesIn", declUnitKey), processor.OutcomeAccepted)
	assertLinkRevived(t, ctx, conn, declLnk, "residesIn", idKey, declUnitKey)

	// The vector: wire → unwire → UNDECLARED re-wire on the second unit. The
	// identity now carries two residesIn entries on its page (one alive, one
	// tombstoned), so the walk has to pick the matching one, not the first.
	undeclLnk := linkKeyOf(idKey, "residesIn", undeclUnitKey)
	submitHint(t, ctx, conn, cp, cons, "slUndeclVec1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": undeclUnitKey},
		wireHint(idKey, "residesIn", undeclUnitKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slUndeclVec2", "UnwireResidesIn",
		map[string]any{"linkKey": undeclLnk}, []string{undeclLnk}, processor.OutcomeAccepted)
	if doc := readDoc(t, ctx, conn, undeclLnk); doc["isDeleted"] != true {
		t.Fatalf("precondition: %s should be tombstoned before the undeclared re-wire; got %v", undeclLnk, doc["isDeleted"])
	}
	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slUndeclVec3", idKey, undeclUnitKey,
		undeclaredWireHint(idKey, undeclUnitKey))
	if outcome != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("undeclared re-wire of a tombstoned residesIn: outcome %q (%s), want accepted", outcome, msg)
	}
	if reply == nil || reply.PrimaryKey != undeclLnk {
		t.Fatalf("undeclared re-wire primaryKey = %+v, want %s", reply, undeclLnk)
	}
	assertLinkRevived(t, ctx, conn, undeclLnk, "residesIn", idKey, undeclUnitKey)

	// The two revived documents are the same document, up to the fields that
	// name the link or the operation that wrote it.
	perLink := map[string]bool{"key": true, "targetVertex": true, "createdAt": true, "createdByOp": true, "lastModifiedAt": true, "lastModifiedByOp": true}
	declDoc := readDoc(t, ctx, conn, declLnk)
	undeclDoc := readDoc(t, ctx, conn, undeclLnk)
	if len(declDoc) != len(undeclDoc) {
		t.Fatalf("revived document field sets differ: declared %v, undeclared %v", declDoc, undeclDoc)
	}
	for field, want := range declDoc {
		got, ok := undeclDoc[field]
		if !ok {
			t.Fatalf("undeclared revive dropped field %q (declared revive wrote %v)", field, want)
		}
		if perLink[field] {
			continue
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("revived document field %q differs: declared %s, undeclared %s", field, wantJSON, gotJSON)
		}
	}
	if undeclDoc["targetVertex"] != undeclUnitKey {
		t.Fatalf("undeclared revive targetVertex = %v, want %s", undeclDoc["targetVertex"], undeclUnitKey)
	}
}

// TestSL_ResidesIn_UndeclaredAliveIsNoOp proves the walk's alive arm: a
// WireResidesIn that does not declare the link key, over a link that is
// alive, commits nothing and returns no primaryKey — the same idempotent
// replay a declared submitter gets from its snapshot.
func TestSL_ResidesIn_UndeclaredAliveIsNoOp(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resundeclalive")

	idID := "SLundecaidQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLundecaunQRHJKMNPQR"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)
	lnk := linkKeyOf(idKey, "residesIn", unitKey)

	submitHint(t, ctx, conn, cp, cons, "slUndeclAlive1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)
	before := readDoc(t, ctx, conn, lnk)

	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slUndeclAlive2", idKey, unitKey,
		undeclaredWireHint(idKey, unitKey))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("undeclared re-wire of an alive residesIn: outcome %q, want accepted (idempotent no-op)", outcome)
	}
	if reply == nil || reply.PrimaryKey != "" {
		t.Fatalf("undeclared re-wire of an alive link surfaced primaryKey %+v, want none (nothing committed)", reply)
	}
	if len(reply.Revisions) != 0 {
		t.Fatalf("undeclared re-wire of an alive link committed keys %v, want none", reply.Revisions)
	}
	after := readDoc(t, ctx, conn, lnk)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("undeclared re-wire of an alive link rewrote it: before %s, after %s", beforeJSON, afterJSON)
	}
}

// TestSL_ResidesIn_UndeclaredFirstWireCreates proves the walk's absent arm: a
// WireResidesIn that does not declare the link key, for an identity that has
// never resided anywhere, creates the link exactly as a declared first wire
// does.
func TestSL_ResidesIn_UndeclaredFirstWireCreates(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resundeclfirst")

	idID := "SLundecfidQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLundecfunQRHJKMNPQR"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)
	lnk := linkKeyOf(idKey, "residesIn", unitKey)

	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slUndeclFirst1", idKey, unitKey,
		undeclaredWireHint(idKey, unitKey))
	if outcome != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("undeclared first wire: outcome %q (%s), want accepted", outcome, msg)
	}
	if reply == nil || reply.PrimaryKey != lnk {
		t.Fatalf("undeclared first wire primaryKey = %+v, want %s", reply, lnk)
	}
	doc := readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("undeclared first wire should create the link alive; got isDeleted=true")
	}
	if doc["class"] != "residesIn" {
		t.Fatalf("created link class = %v, want residesIn", doc["class"])
	}
	if got, _ := doc["sourceVertex"].(string); got != idKey {
		t.Fatalf("created sourceVertex = %q, want %q", got, idKey)
	}
	if got, _ := doc["targetVertex"].(string); got != unitKey {
		t.Fatalf("created targetVertex = %q, want %q", got, unitKey)
	}
	if _, ok := doc["createdAt"]; !ok {
		t.Fatalf("created link carries no createdAt — the walk's absent arm must be a create, not an update: %v", doc)
	}
}

// scriptIntConstant reads a module-level `NAME = <int>` binding out of the
// shipped serviceLocation script, so a fixture sized against the walk's bound
// tracks the constant the script actually runs with.
func scriptIntConstant(t *testing.T, name string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*=\s*([0-9]+)\s*$`)
	m := re.FindStringSubmatch(servicelocation.Package.DDLs[0].Script)
	if m == nil {
		t.Fatalf("serviceLocation script defines no integer constant %s", name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("serviceLocation script constant %s = %q is not an integer: %v", name, m[1], err)
	}
	return n
}

// seedTombstonedResidences writes n tombstoned residesIn links from idKey into
// Core KV directly, on filler units whose keys sort BEFORE any unit id
// starting "SLpgt": kv.Links pages an identity's links in key order, so these
// are the pages a later target has to sit behind.
func seedTombstonedResidences(t *testing.T, ctx context.Context, conn *substrate.Conn, idKey string, n int) {
	t.Helper()
	if n > 26*26 {
		t.Fatalf("seedTombstonedResidences: %d exceeds the two-letter fixture space", n)
	}
	for i := 0; i < n; i++ {
		unitID := "SLpgfill" + fillerLetter(i/26) + fillerLetter(i%26) + "HJKMNPQRST"
		unitKey := "vtx.unit." + unitID
		lnk := linkKeyOf(idKey, "residesIn", unitKey)
		doc := map[string]any{"class": "residesIn", "isDeleted": true, "sourceVertex": idKey, "targetVertex": unitKey,
			"localName": "residesIn", "data": map[string]any{}}
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, lnk, b); err != nil {
			t.Fatalf("seed tombstoned residesIn %s: %v", lnk, err)
		}
	}
}

// fillerLetter maps 0..25 onto NanoID-alphabet characters (keys.Alphabet
// excludes l and o; those two indexes become 1 and 2 instead), every one of
// which sorts below the 't' of the target unit ids.
func fillerLetter(i int) string {
	c := string(rune('a' + i))
	switch c {
	case "l":
		return "1"
	case "o":
		return "2"
	}
	return c
}

// TestSL_ResidesIn_RewireUndeclaredPastFirstPage proves the walk pages: an
// identity with more residesIn subjects than one page holds — every filler a
// tombstone, sorting ahead of the target — still gets its target tombstone
// revived by an undeclared re-wire. Without paging the target sits past the
// first page, the walk sees no entry, and the create is a create-once
// collision on every redelivery.
func TestSL_ResidesIn_RewireUndeclaredPastFirstPage(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resundeclpaged")
	pageLimit := scriptIntConstant(t, "RESIDES_IN_PAGE_LIMIT")

	idID := "SLpgidentQRHJKMNPQRS"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLpgtargetQRHJKMNPQR"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)
	lnk := linkKeyOf(idKey, "residesIn", unitKey)

	seedTombstonedResidences(t, ctx, conn, idKey, pageLimit+1)
	submitHint(t, ctx, conn, cp, cons, "slPaged1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slPaged2", "UnwireResidesIn",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)

	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slPaged3", idKey, unitKey,
		undeclaredWireHint(idKey, unitKey))
	if outcome != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("undeclared re-wire with the tombstone past page 1 (%d fillers): outcome %q (%s), want accepted", pageLimit+1, outcome, msg)
	}
	if reply == nil || reply.PrimaryKey != lnk {
		t.Fatalf("paged undeclared re-wire primaryKey = %+v, want %s", reply, lnk)
	}
	assertLinkRevived(t, ctx, conn, lnk, "residesIn", idKey, unitKey)
}

// TestSL_ResidesIn_PastBoundAbsentCreates proves the walk's fallback past its
// bound is a create: an identity whose residesIn subjects exceed
// RESIDES_IN_MAX_PAGES pages, wired to a unit it has NEVER resided in, gets
// the link created — the bound is never a ceiling on a fresh residence.
func TestSL_ResidesIn_PastBoundAbsentCreates(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "respastboundabsent")
	bound := scriptIntConstant(t, "RESIDES_IN_PAGE_LIMIT") * scriptIntConstant(t, "RESIDES_IN_MAX_PAGES")

	idID := "SLpgabsentQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLpgtargetQRHJKMNPQS"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)
	lnk := linkKeyOf(idKey, "residesIn", unitKey)

	// bound subjects exactly fill the last page and the walk answers
	// "absent"; one more puts the answer past the bound.
	seedTombstonedResidences(t, ctx, conn, idKey, bound+1)
	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slPastBoundAbsent1", idKey, unitKey,
		undeclaredWireHint(idKey, unitKey))
	if outcome != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("wire of an absent residence past the walk's bound (%d subjects): outcome %q (%s), want accepted", bound+1, outcome, msg)
	}
	if reply == nil || reply.PrimaryKey != lnk {
		t.Fatalf("wire past the bound primaryKey = %+v, want %s", reply, lnk)
	}
	doc := readDoc(t, ctx, conn, lnk)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("wire past the bound should create the link alive; got isDeleted=true")
	}
	if _, ok := doc["createdAt"]; !ok {
		t.Fatalf("wire past the bound must be a create, not an update: %v", doc)
	}
}

// TestSL_ResidesIn_PastBoundTombstoneConflicts pins the fallback's other
// outcome: a tombstoned target the bounded walk never reached is re-wired as
// a create, and the create is create-once, so the commit path rejects
// RevisionConflict and the link stays dead — never a blind overwrite, and
// confined to identities carrying more residesIn subjects than the bound.
func TestSL_ResidesIn_PastBoundTombstoneConflicts(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "respastboundtomb")
	bound := scriptIntConstant(t, "RESIDES_IN_PAGE_LIMIT") * scriptIntConstant(t, "RESIDES_IN_MAX_PAGES")

	idID := "SLpgtombstQRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	unitID := "SLpgtargetQRHJKMNPQT"
	unitKey := seedLocation(t, ctx, conn, "unit", unitID)
	lnk := linkKeyOf(idKey, "residesIn", unitKey)

	// The target is tombstoned through the ops (a real unwire, at a real
	// revision) BEFORE the fillers push it past the bound.
	submitHint(t, ctx, conn, cp, cons, "slPastBoundTomb1", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)
	submit(t, ctx, conn, cp, cons, "slPastBoundTomb2", "UnwireResidesIn",
		map[string]any{"linkKey": lnk}, []string{lnk}, processor.OutcomeAccepted)
	before := readDoc(t, ctx, conn, lnk)
	seedTombstonedResidences(t, ctx, conn, idKey, bound)

	outcome, reply := submitWireResidesInReply(t, ctx, conn, cp, cons, "slPastBoundTomb3", idKey, unitKey,
		undeclaredWireHint(idKey, unitKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("undeclared re-wire of a tombstone past the walk's bound: outcome %q, want rejected", outcome)
	}
	if reply == nil || reply.Error == nil {
		t.Fatalf("undeclared re-wire of a tombstone past the bound: no error in the reply %+v", reply)
	}
	if reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("undeclared re-wire of a tombstone past the bound: error code %q (%s), want %q",
			reply.Error.Code, reply.Error.Message, processor.ErrCodeRevisionConflict)
	}
	// The conflict is the create's own assertion (revision 0 against a key
	// already at a later one): step8_commit.go's ConflictError renders it as
	// `expected=0`. The substrate does not name the key for this shape, so
	// the reply's conflictingKey detail is best-effort empty and not pinned.
	if !strings.Contains(reply.Error.Message, "expected=0") {
		t.Fatalf("conflict reply %q should carry the create-once assertion (expected=0)", reply.Error.Message)
	}
	after := readDoc(t, ctx, conn, lnk)
	if del, _ := after["isDeleted"].(bool); !del {
		t.Fatalf("a create-once collision must leave the tombstone dead; got isDeleted=false")
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("a rejected re-wire rewrote the tombstone: before %s, after %s", beforeJSON, afterJSON)
	}
}

// TestSL_WorksAt_RejectsNonLocation proves worksAt carries the SAME location
// guard as residesIn: a workplace that is alive but whose KEY TYPE SEGMENT is
// not a location level is rejected. Without this the staff spine could be
// anchored on an arbitrary vertex, and the workplace-anchored read grants
// derived from it would scope to something that is not a place.
//
// The guard reads the KEY, never the root class. A location vertex's class
// equals its own key type (unit / building / property) while every location
// minted before the taxonomy landed carries the shared class `location`, so no
// class value names the family and a class check would reject one of the two
// live populations. seedLocation's own fixtures — concrete key type, legacy
// shared class — are this test's positive vector, and they pass.
func TestSL_WorksAt_RejectsNonLocation(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "worknonloc")

	idID := "SLstaffnonmocHJKMNPQ"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	notLocID := "SLfakebdgQRHJKMNPQRS"
	notLocKey := "vtx.service." + notLocID
	seedVertex(t, ctx, conn, notLocKey, "service", nil)

	submitHint(t, ctx, conn, cp, cons, "slWorkNonLoc", "WireWorksAt",
		map[string]any{"identity": idKey, "location": notLocKey},
		wireHint(idKey, "worksAt", notLocKey), processor.OutcomeRejected)
}

// TestSL_WorksAt_Multiple proves worksAt cardinality is multiple: one staff
// member may work at two buildings concurrently (the multi-site front desk).
func TestSL_WorksAt_Multiple(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "workmulti")

	idID := "SLmumtiworkRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	bldgAKey := seedLocation(t, ctx, conn, "building", "SLmumtiwaQRHJKMNPQRS")
	bldgBKey := seedLocation(t, ctx, conn, "building", "SLmumtiwbQRHJKMNPQRS")

	submitHint(t, ctx, conn, cp, cons, "slWorkMultiA", "WireWorksAt",
		map[string]any{"identity": idKey, "location": bldgAKey},
		wireHint(idKey, "worksAt", bldgAKey), processor.OutcomeAccepted)
	submitHint(t, ctx, conn, cp, cons, "slWorkMultiB", "WireWorksAt",
		map[string]any{"identity": idKey, "location": bldgBKey},
		wireHint(idKey, "worksAt", bldgBKey), processor.OutcomeAccepted)

	for _, loc := range []string{bldgAKey, bldgBKey} {
		locID := strings.TrimPrefix(loc, "vtx.building.")
		lnk := "lnk.identity." + idID + ".worksAt.building." + locID
		doc := readDoc(t, ctx, conn, lnk)
		if del, _ := doc["isDeleted"].(bool); del {
			t.Fatalf("worksAt link %s should be alive (multiple workplaces allowed)", lnk)
		}
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
	submitHint(t, ctx, conn, cp, cons, "slAvail1", "WireAvailableAt",
		map[string]any{"service": tplKey, "location": bldgKey},
		wireHint(tplKey, "availableAt", bldgKey), processor.OutcomeAccepted)

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
	submitHint(t, ctx, conn, cp, cons, "slUnav1", "WireUnavailableAt",
		map[string]any{"service": tplKey, "location": penthKey},
		wireHint(tplKey, "unavailableAt", penthKey), processor.OutcomeAccepted)

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
	submitHint(t, ctx, conn, cp, cons, "slPerm1", "WirePermitsOperation",
		map[string]any{"service": svcKey, "operation": opKey},
		wireHint(svcKey, "permitsOperation", opKey), processor.OutcomeAccepted)

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

	submitHint(t, ctx, conn, cp, cons, "slAvailInst", "WireAvailableAt",
		map[string]any{"service": instKey, "location": bldgKey},
		wireHint(instKey, "availableAt", bldgKey), processor.OutcomeRejected)

	lnk := "lnk.service." + instID + ".availableAt.building." + bldgID
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lnk); err == nil {
		t.Fatalf("availableAt link from an instance was committed: %s", lnk)
	}
}

// TestSL_ResidesIn_RejectsNonLocation proves the location guard: a residesIn
// whose target is alive but whose KEY TYPE SEGMENT is not a location level is
// rejected. Its positive vector is every other residesIn test in this file,
// which wires a seedLocation endpoint and is accepted.
func TestSL_ResidesIn_RejectsNonLocation(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resnonloc")

	idID := "SLresnonmocRHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	// A live vertex whose type segment is not an admitted location level.
	notLocID := "SLfakeunitQRHJKMNPQR"
	notLocKey := "vtx.service." + notLocID
	seedVertex(t, ctx, conn, notLocKey, "service", nil)

	submitHint(t, ctx, conn, cp, cons, "slResNonLoc", "WireResidesIn",
		map[string]any{"identity": idKey, "location": notLocKey},
		wireHint(idKey, "residesIn", notLocKey), processor.OutcomeRejected)
}

// TestSL_ResidesIn_RejectsForeignClassOnLocationKey is the CLASS arm's own pin
// on an "any location" guard, and the key arm cannot stand in for it: the
// vector is keyed vtx.unit.<NanoID>, so every key-shaped check passes and only
// the class refuses it.
//
// What it protects: `vtx.unit.*` is location-domain's keyspace, but nothing
// stops another package minting a vertex there under a class of its own — and
// a residesIn edge is what the whole capabilityServiceAccess auth plane walks
// from. The positive vectors are seedLocation's own endpoints, whose concrete
// key type carries the shared pre-taxonomy class (the live migration shape)
// and which every other residesIn test here accepts.
func TestSL_ResidesIn_RejectsForeignClassOnLocationKey(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resforeigncls")

	idID := "SLresforeignHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	// A location KEY TYPE carrying a class location-domain never writes.
	foreignID := "SLforeignCLassHJKMNP"
	foreignKey := "vtx.unit." + foreignID
	seedVertex(t, ctx, conn, foreignKey, "service", nil)

	outcome, why := submitHintWithReason(t, ctx, conn, cp, cons, "slResForeign", "WireResidesIn",
		map[string]any{"identity": idKey, "location": foreignKey},
		wireHint(idKey, "residesIn", foreignKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want rejected", outcome)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
}

// TestSL_ResidesIn_AcceptsPerTypeClass pins the class arm's positive case: a
// location vertex's class is its own key type (CreateLocation writes
// make_vtx(loc_key, lt, {})), which is the only shape a live location vertex
// carries — the same shape seedLocation mints, accepted by every other
// residesIn test here.
func TestSL_ResidesIn_AcceptsPerTypeClass(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "respertype")

	idID := "SLrespertypeHJKMNPQR"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	// class == the key's own type segment: what CreateLocation mints now.
	unitID := "SLpertypeUnitHJKMNPQ"
	unitKey := "vtx.unit." + unitID
	seedVertex(t, ctx, conn, unitKey, "unit", nil)

	submitHint(t, ctx, conn, cp, cons, "slResPerType", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitKey},
		wireHint(idKey, "residesIn", unitKey), processor.OutcomeAccepted)
}

// TestSL_ResidesIn_RejectsAdmittedClassOnNonLocationKey discriminates the KEY
// arm from the class arm. Every other negative vector here fails BOTH arms at
// once, so none of them can tell a key-type guard from the class-only guard
// that preceded it. This one carries an admitted class on a key type that is
// not a location at all: only the key arm can refuse it.
func TestSL_ResidesIn_RejectsAdmittedClassOnNonLocationKey(t *testing.T) {
	ctx, conn := setupSLEnv(t)
	cp, cons := newSLPipeline(t, ctx, conn, "resnonlocadmcls")

	idID := "SLresLegacyHJKMNPQRS"
	idKey := "vtx.identity." + idID
	seedVertex(t, ctx, conn, idKey, "identity", map[string]any{"state": "claimed"})
	// An admitted CLASS on a non-location KEY TYPE.
	impostorID := "SLimpostorHJKMNPQRST"
	impostorKey := "vtx.service." + impostorID
	seedVertex(t, ctx, conn, impostorKey, "unit", nil)

	outcome, why := submitHintWithReason(t, ctx, conn, cp, cons, "slResLegacyBad", "WireResidesIn",
		map[string]any{"identity": idKey, "location": impostorKey},
		wireHint(idKey, "residesIn", impostorKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want rejected", outcome)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
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

	submitHint(t, ctx, conn, cp, cons, "slPermNoOp", "WirePermitsOperation",
		map[string]any{"service": svcKey, "operation": badOpKey},
		wireHint(svcKey, "permitsOperation", badOpKey), processor.OutcomeRejected)
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

	submitHint(t, ctx, conn, cp, cons, "slMultiA", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitAKey},
		wireHint(idKey, "residesIn", unitAKey), processor.OutcomeAccepted)
	submitHint(t, ctx, conn, cp, cons, "slMultiB", "WireResidesIn",
		map[string]any{"identity": idKey, "location": unitBKey},
		wireHint(idKey, "residesIn", unitBKey), processor.OutcomeAccepted)

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
