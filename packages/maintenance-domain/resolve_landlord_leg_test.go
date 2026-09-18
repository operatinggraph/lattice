// ResolveWorkOrder's landlord self leg through the real install + Processor
// pipeline: a landlord resolves a work order at a unit they manage on a
// scope=self grant, and the script's management bind — not the capability
// plane, which only proves the target is the caller — is what confines the
// write to the units they manage.
//
// Each negative is paired with the positive it shares a fixture with, and
// every refusal is asserted by the script's own code, so the management bind
// is told apart from the task bind, the staff walk and the terminal branch
// beside it.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	maintenancedomain "github.com/operatinggraph/lattice/packages/maintenance-domain"
)

// mdLandlord is the landlord: a consumer holding ResolveWorkOrder on
// scope=self and nothing else — no staff role, no worksAt link — wired
// manages unit A by the vectors that want a managing landlord.
const (
	mdLandlordID  = "BBMANTLANDLQRDHJKMNP"
	mdLandlordKey = "vtx.identity." + mdLandlordID
	mdLandlordCap = "cap.identity." + mdLandlordID
)

// mdManagesLink is the deterministic management-spine link key the landlord
// dispatcher declares as an OptionalRead.
func mdManagesLink(actorKey, unitKey string) string {
	return "lnk.identity." + actorKey[len("vtx.identity."):] + ".manages.unit." + unitKey[len("vtx.unit."):]
}

// mdLandlordCapDoc is the plain signed-in landlord's capability:
// ResolveWorkOrder on scope=self only. There is deliberately no scope=any
// grant, so what admits every positive vector below is the self grant plus
// the management bind and nothing else. (A dual holder is a different vector:
// the leg keys on the RAW stated target, so a scope=any holder naming
// themselves DOES reach the bind and is held to it —
// TestResolveWorkOrder_OperatorNamingThemselvesIsHeldToTheBind.)
func mdLandlordCapDoc(actorKey string) *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorKey[len("vtx.identity."):],
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ResolveWorkOrder", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{mdConsumerRoleKey()},
	}
}

// mdSeedLandlord seeds the landlord identity, its self cap and the consumer
// role link. manages wires the management spine to unit A — the fact the
// vertical records when a landlord takes on a unit.
func mdSeedLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn, manages bool) {
	t.Helper()
	mdSeedVertex(t, ctx, conn, mdLandlordKey, "identity")
	testutil.SeedCapDoc(t, ctx, conn, mdLandlordCapDoc(mdLandlordKey))
	testutil.SeedHoldsRole(t, ctx, conn, mdLandlordKey, mdConsumerRoleKey())
	if manages {
		testutil.SeedLink(t, ctx, conn, mdManagesLink(mdLandlordKey, mdUnitAKey), "manages", mdLandlordKey, mdUnitAKey)
	}
}

// mdSelfResolveEnvelope is the landlord dispatcher's hand-built submit
// (loftspace-app's Resolve): authContext {target: self}, the work order as a
// required read, its .resolution as an OptionalRead, and the caller's manages
// link to the order's unit declared as an OptionalRead — extraOptional nil
// models a submitter that never declared the link.
func mdSelfResolveEnvelope(label, actorKey, workOrderKey, notes string, extraOptional []string, authContext *processor.AuthContext) *processor.OperationEnvelope {
	b, _ := json.Marshal(map[string]any{"workOrderKey": workOrderKey, "notes": notes})
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ResolveWorkOrder",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T11:30:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Enumerations:  testutil.DeclaredEnumerations("ResolveWorkOrder", actorKey, maintenancedomain.OpMetas()),
			Reads:         []string{workOrderKey},
			OptionalReads: append([]string{workOrderKey + ".resolution"}, extraOptional...),
		},
		AuthContext: authContext,
	}
}

func mdSubmitSelfResolve(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope) (processor.MessageOutcome, string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// mdRequireResolveRefused pins a refusal by the script's own code and that
// the order carries no resolution (or, when before is non-nil, that the
// resolution it already carried was not rewritten).
func mdRequireResolveRefused(t *testing.T, ctx context.Context, conn *substrate.Conn, outcome processor.MessageOutcome, why, code, workOrderKey string, before map[string]any) {
	t.Helper()
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (%s); reply %q", outcome, code, why)
	}
	if !strings.Contains(why, code) {
		t.Fatalf("refused with %q, want the script's own %s", why, code)
	}
	if before == nil {
		if mdKeyLive(ctx, conn, workOrderKey+".resolution") {
			t.Fatalf("a %s refusal committed a resolution; the bind must deny before any mutation", code)
		}
		return
	}
	mdAssertUntouched(t, before, mdReadDoc(t, ctx, conn, workOrderKey+".resolution"), "the "+code+" refusal")
}

// mdReportAt raises a work order at loc as the operator and returns its key.
func mdReportAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, loc, summary, woID string) string {
	t.Helper()
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, label, mdActorKey, loc, summary, woID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue %q = %v, want Accepted", summary, got)
	}
	return "vtx.workorder." + woID
}

// TestResolveWorkOrder_LandlordResolvesAtManagedUnit is the positive vector:
// the landlord, signed in as themselves on the consumer scope=self grant,
// with their manages link declared, resolves a work order at a unit they
// manage. The .resolution records them as resolvedBy — the trusted actor, the
// same field every other leg stamps — and the order's queued task is left
// OPEN by the op: its cancellation is the staleWorkOrderTasks target's
// convergence (stale_task_lens_test.go), never a step in this op.
func TestResolveWorkOrder_LandlordResolvesAtManagedUnit(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordok")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlan00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLAHJKMNPQR")
	mdSeedQueuedTask(t, ctx, conn, woKey)

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlan00000000000002",
		mdLandlordKey, woKey, "Replaced the washer myself.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdLandlordKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("landlord ResolveWorkOrder at a managed unit = %v (%q), want Accepted", outcome, why)
	}
	res := mdReadDoc(t, ctx, conn, woKey+".resolution")
	data, _ := res["data"].(map[string]any)
	if got := data["resolvedBy"]; got != mdLandlordKey {
		t.Errorf("resolution.resolvedBy = %v, want the landlord %s", got, mdLandlordKey)
	}
	if got := data["notes"]; got != "Replaced the washer myself." {
		t.Errorf("resolution.notes = %v, want the submitted notes", got)
	}
	task := mdReadDoc(t, ctx, conn, mdTaskKey)
	if got := task["data"].(map[string]any)["status"]; got != "open" {
		t.Errorf("task status = %v, want open — the op never touches the task; staleWorkOrderTasks cancels it by convergence", got)
	}
}

// TestResolveWorkOrder_LandlordIdenticalNotesReplayIsNoOp: the offline drain's
// vector on the landlord leg — a re-submit carrying the identical notes under
// a fresh requestId reaches the terminal branch through the management bind
// and is the same accepted no-op every other leg gives; different notes are
// still refused AlreadyResolved.
func TestResolveWorkOrder_LandlordIdenticalNotesReplayIsNoOp(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordreplay")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlrp00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLKHJKMNPQR")

	const notes = "Replaced the washer myself."
	submit := func(label, n string) (processor.MessageOutcome, string) {
		return mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope(label,
			mdLandlordKey, woKey, n,
			[]string{mdManagesLink(mdLandlordKey, mdUnitAKey)},
			&processor.AuthContext{Target: mdLandlordKey}))
	}
	if outcome, why := submit("mdlrp00000000000002", notes); outcome != processor.OutcomeAccepted {
		t.Fatalf("first landlord resolve = %v (%q), want Accepted", outcome, why)
	}
	first := mdReadDoc(t, ctx, conn, woKey+".resolution")
	if outcome, why := submit("mdlrp00000000000003", notes); outcome != processor.OutcomeAccepted {
		t.Fatalf("landlord identical-notes replay = %v (%q), want Accepted (idempotent no-op)", outcome, why)
	}
	mdAssertUntouched(t, first, mdReadDoc(t, ctx, conn, woKey+".resolution"), "the landlord's identical-notes replay")
	outcome, why := submit("mdlrp00000000000004", "Actually I replaced the tap.")
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AlreadyResolved", woKey, first)
}

// TestResolveWorkOrder_DeclaredLinkToAnotherUnitIsRefused is the forged-
// optionalRead vector: a landlord who manages unit B declares (and holds)
// their manages link to B while resolving an order at unit A. The bind reads
// the link keyed on the ORDER's own unit, so a link the caller chose to
// declare buys nothing — refused AuthDenied. Its positive sibling: the same
// landlord on an order at B.
func TestResolveWorkOrder_DeclaredLinkToAnotherUnitIsRefused(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordotherunit")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, false)
	mdSeedVertex(t, ctx, conn, mdUnitBKey, "unit")
	testutil.SeedLink(t, ctx, conn, "lnk.unit."+mdUnitBID+".containedIn.building."+mdBuildingAID, "containedIn", mdUnitBKey, mdBuildingAKey)
	testutil.SeedLink(t, ctx, conn, mdManagesLink(mdLandlordKey, mdUnitBKey), "manages", mdLandlordKey, mdUnitBKey)
	atA := mdReportAt(t, ctx, conn, cp, cons, "mdlou00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLLHJKMNPQR")
	atB := mdReportAt(t, ctx, conn, cp, cons, "mdlou00000000000002", mdUnitBKey, "Bathroom fan rattles", "BBMANTWQRKLMHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlou00000000000003",
		mdLandlordKey, atA, "Fixed.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitBKey)},
		&processor.AuthContext{Target: mdLandlordKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", atA, nil)

	outcome, why = mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlou00000000000004",
		mdLandlordKey, atB, "Tightened the housing.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitBKey)},
		&processor.AuthContext{Target: mdLandlordKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("landlord resolving at the unit they DO manage = %v (%q), want Accepted", outcome, why)
	}
}

// TestResolveWorkOrder_OperatorNamingThemselvesIsHeldToTheBind: the leg keys
// on the RAW stated target, so an operator (scope=any, root-exempt on the
// standing shape) who names themselves opts INTO the management bind and is
// held to it — a unit they do not manage refuses AuthDenied. The same
// operator on the standing shape (no target) resolves the same order.
func TestResolveWorkOrder_OperatorNamingThemselvesIsHeldToTheBind(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordoperator")
	mdSeedWorld(t, ctx, conn)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlop00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLNHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlop00000000000002",
		mdActorKey, woKey, "Fixed from the console.",
		[]string{mdManagesLink(mdActorKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdActorKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdlop00000000000003", mdActorKey, woKey, "Fixed from the console.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("operator standing-shape resolve = %v, want Accepted — root is exempt only when it does not name itself", got)
	}
}

// TestResolveWorkOrder_TombstonedManagesLinkIsAbsent: a management link
// tombstoned (the unit changed hands) is served by hydration as a present
// document, and the bind reads it as ABSENT — refused AuthDenied even though
// the caller declared it.
func TestResolveWorkOrder_TombstonedManagesLinkIsAbsent(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordtomb")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	link := mdManagesLink(mdLandlordKey, mdUnitAKey)
	doc := mdReadDoc(t, ctx, conn, link)
	doc["isDeleted"] = true
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, link, b); err != nil {
		t.Fatalf("tombstone manages: %v", err)
	}
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdltb00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLPHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdltb00000000000002",
		mdLandlordKey, woKey, "I used to manage this.",
		[]string{link},
		&processor.AuthContext{Target: mdLandlordKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)
}

// TestResolveWorkOrder_ResidentAtTheUnitIsRefused: a consumer who LIVES at
// the unit (residesIn, the ReportIssue self leg's fact) but manages nothing
// reaches the leg on the same grant shape and is refused AuthDenied — the
// bind reads the manages link, and residence buys nothing here. The refusal
// names no unit.
func TestResolveWorkOrder_ResidentAtTheUnitIsRefused(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordres")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)
	// The resident's cap gains the same scope=self ResolveWorkOrder grant a
	// consumer holds; what tells them from a landlord is the graph.
	doc := mdTenantCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions, processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "self"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlrs00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLBHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlrs00000000000002",
		mdTenantKey, woKey, "I fixed it.",
		[]string{mdManagesLink(mdTenantKey, mdUnitAKey), mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)
	if strings.Contains(why, mdUnitAID) {
		t.Errorf("the refusal names the unit (%q); a denial must not turn into a lookup over where the order is", why)
	}
}

// TestResolveWorkOrder_ForgedTargetOnTheSelfLegIsRefused: two forgeries of
// the target on a self-shaped submit. A scope=self holder naming ANOTHER
// identity never reaches the script — step 3 denies scope=self when target
// != actor. A scope=any holder (the tech, standing grant) naming THEMSELVES
// selects the management bind and is refused AuthDenied there: a self-named
// target opts into the stricter bind, never out of the staff walk, and the
// tech manages nothing.
func TestResolveWorkOrder_ForgedTargetOnTheSelfLegIsRefused(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordforge")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	mdSeedTenant(t, ctx, conn, true)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlfg00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLCHJKMNPQR")

	// The landlord names the resident as the target: denied by the plane.
	outcome, _ := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlfg00000000000002",
		mdLandlordKey, woKey, "Fixed.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("scope=self submit naming another identity = %v, want Rejected", outcome)
	}
	if mdKeyLive(ctx, conn, woKey+".resolution") {
		t.Fatal("a forged-target submit committed a resolution")
	}

	// The tech (standing grant, worksAt building A) names themselves on an
	// order INSIDE their workplace: the self-shaped target selects the
	// management bind, which they cannot clear — refused AuthDenied even
	// where the staff walk would have admitted them.
	doc := mdTechCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions, processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlfg00000000000003",
		mdTechKey, woKey, "Fixed.",
		[]string{mdManagesLink(mdTechKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTechKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)

	// POSITIVE SIBLINGS: the same tech on the standing shape (no target) is
	// admitted by the staff walk, so the refusal above is the bind the
	// self-shaped target selected, not the fixture; and the landlord naming
	// themselves is admitted by the management bind.
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdlfg00000000000004", mdTechKey, woKey, "Fixed by staff.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("tech standing-shape resolve inside their workplace = %v, want Accepted", got)
	}
	woKey2 := mdReportAt(t, ctx, conn, cp, cons, "mdlfg00000000000005", mdUnitAKey, "Bathroom fan rattles", "BBMANTWQRKLDHJKMNPQR")
	outcome, why = mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlfg00000000000006",
		mdLandlordKey, woKey2, "Tightened the housing.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdLandlordKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("landlord self resolve = %v (%q), want Accepted", outcome, why)
	}
}

// TestResolveWorkOrder_UndeclaredManagesLinkFailsClosed: the landlord DOES
// manage unit A, but the submitter never declared the manages link. The bind
// reads from hydration only, so an undeclared link is absent and the resolve
// is refused AuthDenied — never admitted on a lazy live read the envelope did
// not name. The read-drift guard armed on every pipeline would fail this test
// at the read if the script fell through to a live GET of the link (the
// manages shape is not in the guard's baseline for this op), so a PASS here
// is the fail-closed posture itself, not merely the refusal.
func TestResolveWorkOrder_UndeclaredManagesLinkFailsClosed(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordundecl")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlud00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLEHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlud00000000000002",
		mdLandlordKey, woKey, "Fixed.",
		nil,
		&processor.AuthContext{Target: mdLandlordKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)

	// The same submitter, the link declared: accepted. Pairs the refusal with
	// the declaration being the only difference.
	outcome, why = mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlud00000000000003",
		mdLandlordKey, woKey, "Fixed.",
		[]string{mdManagesLink(mdLandlordKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdLandlordKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("the declared sibling = %v (%q), want Accepted — otherwise the undeclared refusal proves nothing", outcome, why)
	}
}

// TestResolveWorkOrder_LandlordCannotResolveAtBuilding: the management spine
// binds an identity to a UNIT, so a building-located order (a staff report)
// is never one a landlord resolves on the self leg — refused AuthDenied by
// the type check, before any link is read. The vector seeds and declares a
// manages link to the BUILDING (a shape no vertical writes) so the refusal is
// attributable to the type check alone: absent the check, the declared link
// would be read and the resolve admitted.
func TestResolveWorkOrder_LandlordCannotResolveAtBuilding(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordbldg")
	mdSeedWorld(t, ctx, conn)
	mdSeedLandlord(t, ctx, conn, true)
	buildingLink := "lnk.identity." + mdLandlordID + ".manages.building." + mdBuildingAID
	testutil.SeedLink(t, ctx, conn, buildingLink, "manages", mdLandlordKey, mdBuildingAKey)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlbd00000000000001", mdBuildingAKey, "Lobby light out", "BBMANTWQRKLFHJKMNPQR")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlbd00000000000002",
		mdLandlordKey, woKey, "Changed the bulb.",
		[]string{buildingLink},
		&processor.AuthContext{Target: mdLandlordKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, nil)
}

// TestResolveWorkOrder_UnauthorizedCallerOnResolvedOrderIsDeniedNotAlreadyResolved
// pins the refusal ORDER: the management bind runs ahead of the terminal
// branch, so a caller who may not resolve the order cannot read its
// resolution state either — a non-managing consumer probing a RESOLVED order
// with different notes is refused AuthDenied, never AlreadyResolved, and the
// recorded resolution is untouched.
func TestResolveWorkOrder_UnauthorizedCallerOnResolvedOrderIsDeniedNotAlreadyResolved(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordorder")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)
	doc := mdTenantCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions, processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "self"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	woKey := mdReportAt(t, ctx, conn, cp, cons, "mdlor00000000000001", mdUnitAKey, "Kitchen tap is dripping", "BBMANTWQRKLGHJKMNPQR")
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdlor00000000000002", mdActorKey, woKey, "Replaced the washer.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("operator resolve = %v, want Accepted", got)
	}
	before := mdReadDoc(t, ctx, conn, woKey+".resolution")

	outcome, why := mdSubmitSelfResolve(t, ctx, conn, cp, cons, mdSelfResolveEnvelope("mdlor00000000000003",
		mdTenantKey, woKey, "Guessing at the notes.",
		[]string{mdManagesLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireResolveRefused(t, ctx, conn, outcome, why, "AuthDenied", woKey, before)
	if strings.Contains(why, "AlreadyResolved") {
		t.Fatalf("the terminal branch answered before the bind: %q", why)
	}
}

// TestResolveWorkOrder_TaskAndOperatorPathsStillAccepted is the regression
// half beside the self leg: the task claimant (validated target = the order)
// and the operator (standing, no target) are admitted exactly as before, and
// the task path still auto-completes its task.
func TestResolveWorkOrder_TaskAndOperatorPathsStillAccepted(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlandlordregress")
	mdSeedWorld(t, ctx, conn)

	taskOrder := mdReportAt(t, ctx, conn, cp, cons, "mdlrg00000000000001", mdBuildingBKey, "Lift is out at B", "BBMANTWQRKLHHJKMNPQR")
	mdSeedQueuedTask(t, ctx, conn, taskOrder)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, mdTechTaskGrantDoc(taskOrder))
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdlrg00000000000002", mdTechKey, taskOrder, "Reset the controller.", mdTaskKey); got != processor.OutcomeAccepted {
		t.Fatalf("task-path resolve at a building the tech does not work at = %v, want Accepted (the resource bind)", got)
	}
	if got := mdReadDoc(t, ctx, conn, mdTaskKey)["data"].(map[string]any)["status"]; got != "complete" {
		t.Errorf("task status = %v, want complete — the §10.6 auto-complete", got)
	}

	opOrder := mdReportAt(t, ctx, conn, cp, cons, "mdlrg00000000000003", mdUnitAKey, "Tap dripping at A", "BBMANTWQRKLJHJKMNPQR")
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdlrg00000000000004", mdActorKey, opOrder, "Replaced the washer.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("operator standing resolve = %v, want Accepted", got)
	}
}
