// SetListingStatus's landlord path. A signed-in landlord holds no operator role
// and authorizes the transition through a scope=self grant, so their `manages`
// link to the unit is the only thing confining them. The convergence directOp
// that drives a unit to leased carries no authContext and must stay untouched.
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

// llLandlordCapDoc is the plain signed-in landlord: `consumer` plus a scope=SELF
// SetListingStatus grant and nothing else, so step 3 denies unless
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
			{OperationType: "SetListingStatus", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// llSetListingStatusAsLandlord submits the transition as the landlord acting as
// themselves (authContext.target == actor — the only shape a scope=self grant
// authorizes at all) and returns the reply so a test can see which check
// answered.
func llSetListingStatusAsLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, unitKey, status string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"unit": unitKey, "status": status})
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetListingStatus",
		Actor:         llLandlordKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         "loftspaceListing",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads:         []string{unitKey, unitKey + ".listing"},
			OptionalReads: []string{"lnk.identity." + llLandlordID + ".manages.unit." + unitID},
		},
		AuthContext: &processor.AuthContext{Target: llLandlordKey},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
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
		Class:         "location",
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
	testutil.SeedCapDoc(t, ctx, conn, llLandlordCapDoc())
	lsSeedVertex(t, ctx, conn, llLandlordKey, "identity", false)
	testutil.SeedHoldsRole(t, ctx, conn, llLandlordKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))

	mine := llCreateUnit(t, ctx, conn, cp, cons, "llUnitMine")
	theirs := llCreateUnit(t, ctx, conn, cp, cons, "llUnitTheirs")
	setListing(t, ctx, conn, cp, cons, "llList1", mine,
		strings.Replace(llListingPayload, "%s", mine, 1), processor.OutcomeAccepted)
	setListing(t, ctx, conn, cp, cons, "llList2", theirs,
		strings.Replace(llListingPayload, "%s", theirs, 1), processor.OutcomeAccepted)
	assignUnitOwner(t, ctx, conn, cp, cons, "llAssign1", llLandlordKey, mine,
		lsStaffActorKey, processor.OutcomeAccepted)

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
