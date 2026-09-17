// ReportIssue's consumer self leg through the real install + Processor
// pipeline: a resident reports an issue at the unit they reside in on a
// scope=self grant, and the script's residence bind — not the capability
// plane, which only proves the target is the caller — is what confines the
// write to their own home.
//
// Each negative is paired with the positive it shares a fixture with — a
// refusal that lands because the path is broken for an unrelated reason
// proves nothing — and every refusal is asserted by the script's own code, so
// the residence bind is told apart from the location guard and the staff walk
// beside it.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	maintenancedomain "github.com/operatinggraph/lattice/packages/maintenance-domain"
)

// mdTenant is the resident: a consumer holding ReportIssue on scope=self and
// nothing else — no staff role, no worksAt link — wired residesIn unit A by
// the vectors that want a resident. mdUnitB is a second unit in building A
// the tenant does NOT reside in.
const (
	mdTenantID  = "BBMANTTENANTHJKMNPQR"
	mdTenantKey = "vtx.identity." + mdTenantID
	mdTenantCap = "cap.identity." + mdTenantID

	mdUnitBID  = "BBMANTUNTBHJKMNPQRST"
	mdUnitBKey = "vtx.unit." + mdUnitBID
)

func mdConsumerRoleKey() string {
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")
}

// mdResidesInLink is the deterministic residence-spine link key the tenant
// dispatcher declares as an OptionalRead.
func mdResidesInLink(actorKey, unitKey string) string {
	return "lnk.identity." + actorKey[len("vtx.identity."):] + ".residesIn.unit." + unitKey[len("vtx.unit."):]
}

// mdTenantCapDoc is the plain signed-in resident's capability: ReportIssue on
// scope=self only. There is deliberately no scope=any grant — the vectors
// below prove the SELF leg, and a holder of both would be authorized on the
// standing grant with authTargetValidated false, never reaching it.
func mdTenantCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    mdTenantCap,
		Actor:                  mdTenantKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdTenantKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ReportIssue", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{mdConsumerRoleKey()},
	}
}

// mdSeedTenant seeds the resident identity, its self cap, the consumer role
// link, and unit B beside unit A in building A. resident wires the residesIn
// spine to unit A — the fact WireResidesIn records at lease approval.
func mdSeedTenant(t *testing.T, ctx context.Context, conn *substrate.Conn, resident bool) {
	t.Helper()
	mdSeedVertex(t, ctx, conn, mdTenantKey, "identity")
	mdSeedVertex(t, ctx, conn, mdUnitBKey, "unit")
	testutil.SeedLink(t, ctx, conn,
		"lnk.unit."+mdUnitBID+".containedIn.building."+mdBuildingAID,
		"containedIn", mdUnitBKey, mdBuildingAKey)
	testutil.SeedCapDoc(t, ctx, conn, mdTenantCapDoc())
	testutil.SeedHoldsRole(t, ctx, conn, mdTenantKey, mdConsumerRoleKey())
	if resident {
		testutil.SeedLink(t, ctx, conn, mdResidesInLink(mdTenantKey, mdUnitAKey), "residesIn", mdTenantKey, mdUnitAKey)
	}
}

// mdSelfEnvelope is the tenant dispatcher's hand-built submit (loftspace-app's
// "Report an issue"): authContext {target: self}, the location as a required
// read, and the caller's residesIn link to it declared as an OptionalRead —
// optionalReads nil models a submitter that never declared the link.
func mdSelfEnvelope(label, actorKey, location, summary, workOrderID string, optionalReads []string, authContext *processor.AuthContext) *processor.OperationEnvelope {
	payload := map[string]any{"summary": summary, "location": location}
	if workOrderID != "" {
		payload["workOrderId"] = workOrderID
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ReportIssue",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T09:00:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Enumerations:  testutil.DeclaredEnumerations("ReportIssue", actorKey, maintenancedomain.OpMetas()),
			Reads:         []string{location},
			OptionalReads: optionalReads,
		},
		AuthContext: authContext,
	}
}

func mdSubmitSelf(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope) (processor.MessageOutcome, string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

func mdRequireRefused(t *testing.T, outcome processor.MessageOutcome, why, code, workOrderID string, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (%s); reply %q", outcome, code, why)
	}
	if !strings.Contains(why, code) {
		t.Fatalf("refused with %q, want the script's own %s", why, code)
	}
	if mdKeyLive(ctx, conn, "vtx.workorder."+workOrderID) {
		t.Fatalf("a %s refusal committed a work order; the bind must deny before any mutation", code)
	}
}

// TestReportIssue_ResidentReportsAtOwnUnit is the positive vector: the
// resident, signed in as themselves on the consumer scope=self grant, with
// their residesIn link declared, raises a work order at their own unit. The
// .report records them as reportedBy — the trusted actor, the same field the
// staff leg stamps — and the locatedAt link lands on the unit.
func TestReportIssue_ResidentReportsAtOwnUnit(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfok")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	const woID = "BBMANTWQRKSAHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000001",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", woID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("resident ReportIssue at their own unit = %v (%q), want Accepted", outcome, why)
	}
	woKey := "vtx.workorder." + woID
	if !mdKeyLive(ctx, conn, woKey) {
		t.Fatalf("%s: work order not committed", woKey)
	}
	report := mdReadDoc(t, ctx, conn, woKey+".report")
	data, _ := report["data"].(map[string]any)
	if got := data["reportedBy"]; got != mdTenantKey {
		t.Errorf("report.reportedBy = %v, want the resident %s", got, mdTenantKey)
	}
	if got := data["summary"]; got != "Kitchen tap is dripping" {
		t.Errorf("report.summary = %v, want the reported summary", got)
	}
	if !mdKeyLive(ctx, conn, "lnk.workorder."+woID+".locatedAt.unit."+mdUnitAID) {
		t.Errorf("locatedAt link not committed — the work order has no place")
	}
}

// TestReportIssue_NonResidentIsRefused: the same grant, the same declared
// link, no residence — the declared OptionalRead is known-absent at the
// step-4 snapshot, the script reads it as None, and the report is refused
// NotResident before any mutation.
func TestReportIssue_NonResidentIsRefused(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfnonres")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, false)

	const woID = "BBMANTWQRKSBHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000002",
		mdTenantKey, mdUnitAKey, "Not my home", woID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireRefused(t, outcome, why, "NotResident", woID, ctx, conn)
}

// TestReportIssue_ResidentCannotReportAtBuilding: the residence spine binds an
// identity to a UNIT, so a building — even the one containing the resident's
// unit — is never a place a consumer reports at. Refused NotResident by the
// type check, before any link is read. The vector seeds and declares a
// residesIn link to the BUILDING — a shape WireResidesIn never writes — so
// the refusal is attributable to the type check alone: absent the check, the
// declared link would be read live and the report admitted.
func TestReportIssue_ResidentCannotReportAtBuilding(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfbldg")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)
	buildingLink := "lnk.identity." + mdTenantID + ".residesIn.building." + mdBuildingAID
	testutil.SeedLink(t, ctx, conn, buildingLink, "residesIn", mdTenantKey, mdBuildingAKey)

	const woID = "BBMANTWQRKSCHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000003",
		mdTenantKey, mdBuildingAKey, "Lobby light out", woID,
		[]string{buildingLink},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireRefused(t, outcome, why, "NotResident", woID, ctx, conn)
}

// TestReportIssue_ResidentCannotReportAtAnotherUnit: a resident of unit A
// naming unit B (same building) is refused — the link the bind reads is keyed
// on the SUBMITTED unit, so residence somewhere else buys nothing here.
func TestReportIssue_ResidentCannotReportAtAnotherUnit(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfother")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	const woID = "BBMANTWQRKSDHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000004",
		mdTenantKey, mdUnitBKey, "The neighbour's tap", woID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitBKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireRefused(t, outcome, why, "NotResident", woID, ctx, conn)
}

// TestReportIssue_UndeclaredResidenceLinkFailsClosed: the resident DOES live
// at unit A, but the submitter never declared the residesIn link. The bind
// reads from hydration only, so an undeclared link is absent and the report
// is refused NotResident — never admitted on a lazy live read the envelope
// did not name. The read-drift guard armed on every pipeline would fail this
// test at the read if the script fell through to a live GET, so a PASS here
// is the fail-closed posture itself, not merely the refusal.
func TestReportIssue_UndeclaredResidenceLinkFailsClosed(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfundecl")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	const woID = "BBMANTWQRKSEHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000005",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", woID,
		nil,
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireRefused(t, outcome, why, "NotResident", woID, ctx, conn)

	// The same submitter, the link declared: accepted. Pairs the refusal with
	// the declaration being the only difference.
	const okID = "BBMANTWQRKSFHJKMNPQR"
	outcome, why = mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000006",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", okID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("the declared sibling = %v (%q), want Accepted — otherwise the undeclared refusal proves nothing", outcome, why)
	}
}

// TestReportIssue_TombstonedResidenceIsAbsent: UnwireResidesIn tombstones the
// link rather than deleting it, and kv hydration serves the tombstone as a
// present document. A moved-out resident's link is therefore read as ABSENT
// by the bind — the property-2 trap the guard's own doc names.
func TestReportIssue_TombstonedResidenceIsAbsent(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselftomb")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	link := mdResidesInLink(mdTenantKey, mdUnitAKey)
	doc := mdReadDoc(t, ctx, conn, link)
	doc["isDeleted"] = true
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, link, b); err != nil {
		t.Fatalf("tombstone residesIn: %v", err)
	}

	const woID = "BBMANTWQRKSGHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000007",
		mdTenantKey, mdUnitAKey, "I moved out", woID,
		[]string{link},
		&processor.AuthContext{Target: mdTenantKey}))
	mdRequireRefused(t, outcome, why, "NotResident", woID, ctx, conn)
}

// TestReportIssue_ValidatedTargetThatIsNotTheCallerIsRefused: ReportIssue
// carries an op-meta, so a task naming it as forOperation would hand its
// claimant a VALIDATED target that is the unit, not the caller. The self leg
// binds target == actor first, so that grant — even held by a genuine
// resident of the very unit it names — is refused AuthDenied rather than
// exempting the claimant from every location check.
func TestReportIssue_ValidatedTargetThatIsNotTheCallerIsRefused(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselftask")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.ephemeral.identity." + mdTenantID,
		Actor:                  mdTenantKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdTenantKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{{
			Source:        mdTaskKey,
			TaskKey:       mdTaskKey,
			OperationType: "ReportIssue",
			Target:        mdUnitAKey,
			ExpiresAt:     now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		}},
	})

	const woID = "BBMANTWQRKSHHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000008",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", woID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Task: mdTaskKey, Target: mdUnitAKey}))
	mdRequireRefused(t, outcome, why, "AuthDenied", woID, ctx, conn)

	// POSITIVE SIBLING: the same resident on the self path is admitted, so the
	// refusal above is the target bind and not the fixture.
	const okID = "BBMANTWQRKSJHJKMNPQR"
	outcome, why = mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000009",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", okID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("resident on the self path = %v (%q), want Accepted", outcome, why)
	}
}

// TestReportIssue_SelfGrantWithoutTargetIsDenied pins the capability plane's
// half of the leg: a scope=self holder that sends no authContext at all never
// reaches the script — step 3 denies the self grant outright, and the tenant
// holds no standing grant to fall back on.
func TestReportIssue_SelfGrantWithoutTargetIsDenied(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdselfnotarget")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	const woID = "BBMANTWQRKSKHJKMNPQR"
	outcome, _ := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdslf00000000000010",
		mdTenantKey, mdUnitAKey, "Kitchen tap is dripping", woID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		nil))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("scope=self submit with no authContext = %v, want Rejected", outcome)
	}
	if mdKeyLive(ctx, conn, "vtx.workorder."+woID) {
		t.Fatal("a denied submit committed a work order")
	}
}

// mdDualHat is the staff-resident: a backOfHouse tech who worksAt building B
// AND resides at unit A in building A, holding ReportIssue on BOTH scope=any
// (the staff grant) and scope=self (the consumer grant).
const (
	mdDualHatID  = "BBMANTDUALHATHJKMNPQ"
	mdDualHatKey = "vtx.identity." + mdDualHatID
	mdDualHatCap = "cap.identity." + mdDualHatID
)

// mdSeedDualHat seeds the dual-hat actor. The scope=any row is written FIRST
// in the capability so step 3 meets it first and authorizes the tenant card's
// shape on the STAFF grant (authTargetValidated false) — the ordering under
// which a bind keyed on the validated bit would send a self-named target down
// the worksAt walk instead of the residence bind.
func mdSeedDualHat(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	mdSeedVertex(t, ctx, conn, mdDualHatKey, "identity")
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    mdDualHatCap,
		Actor:                  mdDualHatKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdDualHatKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ReportIssue", Scope: "any"},
			{OperationType: "ReportIssue", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles: []string{
			"vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse"),
			mdConsumerRoleKey(),
		},
	})
	testutil.SeedHoldsRole(t, ctx, conn, mdDualHatKey, "vtx.role."+pkgmgr.RoleID("identity-domain", "backOfHouse"))
	testutil.SeedHoldsRole(t, ctx, conn, mdDualHatKey, mdConsumerRoleKey())
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+mdDualHatID+".worksAt.building."+mdBuildingBID,
		"worksAt", mdDualHatKey, mdBuildingBKey)
	testutil.SeedLink(t, ctx, conn, mdResidesInLink(mdDualHatKey, mdUnitAKey), "residesIn", mdDualHatKey, mdUnitAKey)
}

// TestReportIssue_DualHatBindFollowsTheStatedIntent: which grant row step 3
// meets first is a holdsRole-write-order accident, so the bind a submission
// lands on is selected by the shape the caller SENT, not by the row that
// authorized it. A staff-resident reporting from the tenant card (target =
// self) at their own unit — a unit their workplace does NOT cover — is
// admitted on residence; the same shape at another unit in the same building
// is NotResident (the staff walk never runs on that shape, and would not have
// covered it either); and the same actor's standing shape (no target) at their
// workplace is the staff leg, admitted as before.
func TestReportIssue_DualHatBindFollowsTheStatedIntent(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mddualhat")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, false)
	mdSeedDualHat(t, ctx, conn)

	// Tenant card at home: residence admits it, whichever grant authorized.
	const homeID = "BBMANTWQRKDAHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mddual0000000000001",
		mdDualHatKey, mdUnitAKey, "Kitchen tap is dripping", homeID,
		[]string{mdResidesInLink(mdDualHatKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdDualHatKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("staff-resident ReportIssue at their OWN unit from the tenant card = %v (%q), want Accepted — "+
			"the self-named target selects the residence bind, not the worksAt walk the staff grant would run", outcome, why)
	}
	report := mdReadDoc(t, ctx, conn, "vtx.workorder."+homeID+".report")
	if got := report["data"].(map[string]any)["reportedBy"]; got != mdDualHatKey {
		t.Errorf("report.reportedBy = %v, want %s", got, mdDualHatKey)
	}

	// Tenant card at the neighbour's unit: the residence bind refuses.
	const otherID = "BBMANTWQRKDBHJKMNPQR"
	outcome, why = mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mddual0000000000002",
		mdDualHatKey, mdUnitBKey, "The neighbour's tap", otherID,
		[]string{mdResidesInLink(mdDualHatKey, mdUnitBKey)},
		&processor.AuthContext{Target: mdDualHatKey}))
	mdRequireRefused(t, outcome, why, "NotResident", otherID, ctx, conn)

	// Standing shape at the workplace: the staff leg, exactly as before.
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mddual0000000000003",
		mdDualHatKey, mdBuildingBKey, "Lift is out at B", "BBMANTWQRKDCHJKMNPQR"); got != processor.OutcomeAccepted {
		t.Fatalf("staff-resident standing ReportIssue at their workplace = %v, want Accepted", got)
	}
}
