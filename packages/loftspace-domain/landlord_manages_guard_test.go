// The landlord path of the three listing ops. A signed-in landlord holds no
// operator role and authorizes SetListing / SetUnitAddress / SetListingStatus
// through a scope=self grant, so their `manages` link to the unit is the only
// thing confining them. The convergence directOp that drives a unit to leased
// carries no authContext and must stay untouched.
package loftspacedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	llLandlordID  = "LSmanagerHJKMNPQRSTU"
	llLandlordKey = "vtx.identity." + llLandlordID
	llLandlordCap = "cap.identity." + llLandlordID
)

// llLandlordCapDoc is the plain signed-in landlord: `consumer` plus the three
// scope=SELF listing grants and nothing else, so step 3 denies unless
// authContext.target == actor — the path the manages probe binds.
func llLandlordCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    llLandlordCap,
		Actor:                  llLandlordKey,
		Version:                "1.0",
		ProjectedAt:            "2026-07-20T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{llLandlordKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SetListing", Scope: "self"},
			{OperationType: "SetUnitAddress", Scope: "self"},
			{OperationType: "SetListingStatus", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// llSubmitAsLandlord submits one of the listing ops as the landlord acting as
// themselves (authContext.target == actor — the only shape a scope=self grant
// authorizes at all) and returns the reply so a test can see which check
// answered. reads mirrors what the shipped loftspace-app declares for that op;
// the manages link rides optionalReads at every landlord dispatch.
func llSubmitAsLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, opType, unitKey string, reads []string, payload map[string]any) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	body, _ := json.Marshal(payload)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         llLandlordKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         "loftspaceListing",
		Payload:       json.RawMessage(body),
		ContextHint: &processor.ContextHint{
			Reads:         reads,
			OptionalReads: []string{"lnk.identity." + llLandlordID + ".manages.unit." + unitID},
		},
		AuthContext: &processor.AuthContext{Target: llLandlordKey},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

func llSetListingStatusAsLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, unitKey, status string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	return llSubmitAsLandlord(t, ctx, conn, cp, cons, label, "SetListingStatus", unitKey,
		[]string{unitKey, unitKey + ".listing"}, map[string]any{"unit": unitKey, "status": status})
}

// llCreateUnit mints a unit under a caller-supplied label; createUnit's own
// label is fixed, so two calls in one test would collide on the requestId.
func llCreateUnit(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	unitKey := "vtx.unit." + lsNanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLocation",
		Actor:         lsStaffActorKey,
		SubmittedAt:   "2026-07-20T11:00:00Z",
		Class:         "unit",
		Payload:       json.RawMessage(`{"locationType":"unit"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return unitKey
}

const llListingPayload = `{"unit":"%s","rentAmount":2400,"rentCurrency":"USD","bedrooms":2,` +
	`"availableFrom":"2026-09-01T00:00:00Z","leaseTermMonths":12,"status":"available"}`

// TestLandlord_SetListingStatusConfinedToManagedUnit: the landlord transitions
// the unit they manage and is denied on the one they do not, and the denial is
// the ownership guard rather than a downstream check.
func TestLandlord_SetListingStatusConfinedToManagedUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "llmanages")
	mine, theirs := llSeedManagedPair(t, ctx, conn, cp, cons, "ll")

	// Positive sibling first: a Rejected below is then the manages probe
	// talking, not a broken scope=self path.
	if got, _ := llSetListingStatusAsLandlord(t, ctx, conn, cp, cons, "llStatus1", mine, "leased"); got != processor.OutcomeAccepted {
		t.Fatalf("landlord transitions the unit they MANAGE = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	got, reply := llSetListingStatusAsLandlord(t, ctx, conn, cp, cons, "llStatus2", theirs, "leased")
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord transitions a unit they do NOT manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the unmanaged transition rejected with %+v, want AuthDenied — the ownership "+
			"probe must answer before require_live_unit, or this op reports whether a unit exists", reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, theirs+".listing"); doc["data"].(map[string]any)["status"] != "available" {
		t.Errorf("the denied transition changed %s.listing.status", theirs)
	}
}

// llSeedManagedPair seeds the signed-in landlord, two listed units, and the
// landlord's manages link to the first only. Every confinement test below
// starts from this pair so the negative vector (the unmanaged unit) is refused
// by the probe, never by a unit the test forgot to list.
func llSeedManagedPair(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label string) (mine, theirs string) {
	t.Helper()
	testutil.SeedCapDoc(t, ctx, conn, llLandlordCapDoc())
	lsSeedVertex(t, ctx, conn, llLandlordKey, "identity", false)
	testutil.SeedHoldsRole(t, ctx, conn, llLandlordKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
	mine = llCreateUnit(t, ctx, conn, cp, cons, label+"UnitMine")
	theirs = llCreateUnit(t, ctx, conn, cp, cons, label+"UnitTheirs")
	setListing(t, ctx, conn, cp, cons, label+"List1", mine,
		strings.Replace(llListingPayload, "%s", mine, 1), processor.OutcomeAccepted)
	setListing(t, ctx, conn, cp, cons, label+"List2", theirs,
		strings.Replace(llListingPayload, "%s", theirs, 1), processor.OutcomeAccepted)
	assignUnitOwner(t, ctx, conn, cp, cons, label+"Assign1", llLandlordKey, mine,
		lsStaffActorKey, processor.OutcomeAccepted)
	return mine, theirs
}

// llListingEdit is the landlord's edit of a listed unit's economics: a rent
// change on an otherwise-unchanged listing.
func llListingEdit(unitKey string) map[string]any {
	return map[string]any{
		"unit": unitKey, "rentAmount": 2600, "rentCurrency": "USD", "bedrooms": 2,
		"availableFrom": "2026-09-01T00:00:00Z", "leaseTermMonths": 12, "status": "available",
	}
}

// llAddressEdit is the landlord's address correction on a listed unit.
func llAddressEdit(unitKey string) map[string]any {
	return map[string]any{
		"unit": unitKey, "line1": "12 Harbor Row", "city": "Portland", "region": "OR", "postal": "97209",
	}
}

// TestLandlord_SetListingConfinedToManagedUnit: the landlord rewrites the
// economics of the unit they manage and is denied on the one they do not, by
// the ownership guard rather than a downstream check; the denied write leaves
// the other unit's listing untouched.
func TestLandlord_SetListingConfinedToManagedUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "llsetlisting")
	mine, theirs := llSeedManagedPair(t, ctx, conn, cp, cons, "llSL")

	// Positive sibling first: a Rejected below is then the manages probe
	// talking, not a broken scope=self path.
	if got, reply := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llSL1", "SetListing", mine,
		[]string{mine}, llListingEdit(mine)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord edits the listing of the unit they MANAGE = %v (%+v), want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got, reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, mine+".listing"); doc["data"].(map[string]any)["rentAmount"] != float64(2600) {
		t.Fatalf("the landlord's accepted edit did not land on %s.listing: %v", mine, doc["data"])
	}
	got, reply := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llSL2", "SetListing", theirs,
		[]string{theirs}, llListingEdit(theirs))
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord edits the listing of a unit they do NOT manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the unmanaged edit rejected with %+v, want AuthDenied — the ownership "+
			"probe must answer before require_live_unit, or this op reports whether a unit exists", reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, theirs+".listing"); doc["data"].(map[string]any)["rentAmount"] != float64(2400) {
		t.Errorf("the denied edit changed %s.listing.rentAmount", theirs)
	}
}

// TestLandlord_SetUnitAddressConfinedToManagedUnit: the same confinement on
// the address write — a landlord corrects the address of the unit they manage
// and is denied on the one they do not, before the unit's liveness is checked.
func TestLandlord_SetUnitAddressConfinedToManagedUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "llsetaddress")
	mine, theirs := llSeedManagedPair(t, ctx, conn, cp, cons, "llSA")

	if got, reply := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llSA1", "SetUnitAddress", mine,
		[]string{mine}, llAddressEdit(mine)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord sets the address of the unit they MANAGE = %v (%+v), want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got, reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, mine+".address"); doc["data"].(map[string]any)["line1"] != "12 Harbor Row" {
		t.Fatalf("the landlord's accepted address did not land on %s.address: %v", mine, doc["data"])
	}
	got, reply := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llSA2", "SetUnitAddress", theirs,
		[]string{theirs}, llAddressEdit(theirs))
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord sets the address of a unit they do NOT manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the unmanaged address write rejected with %+v, want AuthDenied", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, theirs+".address"); err == nil {
		t.Errorf("the denied address write minted %s.address", theirs)
	}
}

// TestLandlord_ProbeAnswersBeforeLiveness: on a TOMBSTONED unit the landlord
// does not manage, all three listing ops refuse with the probe's AuthDenied,
// never UnknownUnit — a live-listed negative vector cannot tell the two
// orderings apart, so this is the vector that pins "the probe answers before
// require_live_unit". A tombstoned key hydrates (it is a document, not a
// miss), so the script runs and the ordering is what decides the code.
func TestLandlord_ProbeAnswersBeforeLiveness(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "llprobefirst")
	testutil.SeedCapDoc(t, ctx, conn, llLandlordCapDoc())
	lsSeedVertex(t, ctx, conn, llLandlordKey, "identity", false)
	testutil.SeedHoldsRole(t, ctx, conn, llLandlordKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
	dead := "vtx.unit.LSdeadunitHJKMNPQRST"
	lsSeedVertex(t, ctx, conn, dead, "unit", true)

	for _, tc := range []struct {
		op      string
		reads   []string
		payload map[string]any
	}{
		{"SetListing", []string{dead}, llListingEdit(dead)},
		{"SetUnitAddress", []string{dead}, llAddressEdit(dead)},
		{"SetListingStatus", []string{dead, dead + ".listing"}, map[string]any{"unit": dead, "status": "withdrawn"}},
	} {
		got, reply := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llProbe"+tc.op, tc.op, dead, tc.reads, tc.payload)
		if got != processor.OutcomeRejected || reply.Error == nil {
			t.Fatalf("%s on a tombstoned unmanaged unit = %v (%+v), want Rejected", tc.op, got, reply)
		}
		if !strings.Contains(reply.Error.Message, "AuthDenied:") || strings.Contains(reply.Error.Message, "UnknownUnit") {
			t.Fatalf("%s on a tombstoned unmanaged unit rejected with %q, want the probe's AuthDenied — "+
				"require_live_unit answered first, so this op reports whether a unit exists", tc.op, reply.Error.Message)
		}
	}
}

// TestLandlord_ConvergenceDirectOpUnaffected: the leaseApplicationComplete
// directOp runs as Weaver's service actor with NO authContext, so the guard is
// inert on it — the automated path that leases a unit on approval must not
// start requiring a management link.
func TestLandlord_ConvergenceDirectOpUnaffected(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "lldirectop")

	unitKey := llCreateUnit(t, ctx, conn, cp, cons, "llUnitDirect")
	setListing(t, ctx, conn, cp, cons, "llDirect1", unitKey,
		strings.Replace(llListingPayload, "%s", unitKey, 1), processor.OutcomeAccepted)

	// class="" is the directOp dispatch shape, and nobody manages this unit.
	setListingStatus(t, ctx, conn, cp, cons, "llDirect2", unitKey, "",
		`{"unit":"`+unitKey+`","status":"leased"}`, processor.OutcomeAccepted)
	if doc := lsReadDoc(t, ctx, conn, unitKey+".listing"); doc["data"].(map[string]any)["status"] != "leased" {
		t.Fatalf("the convergence directOp did not lease an unmanaged unit; the ownership guard must be inert without an authContext")
	}
}
