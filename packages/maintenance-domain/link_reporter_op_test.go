// LinkWorkOrderReporter — the reportedBy backfill — and ReportIssue's own
// link mint, through the real install + Processor pipeline: the literal link
// key both writers mint, the CreateOnly collision between them, and the
// backfill's refusals.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// mdReportedByLink is the deterministic reporter link key ReportIssue mints
// beside the root and LinkWorkOrderReporter backfills.
func mdReportedByLink(workOrderKey, actorKey string) string {
	return "lnk.workorder." + workOrderKey[len("vtx.workorder."):] + ".reportedBy.identity." + actorKey[len("vtx.identity."):]
}

// mdWeaverCapDoc grants Weaver's primordial dispatch actor the two directOps
// this package's playbooks dispatch under it. Read through a func, not a
// package var: bootstrap's primordial globals are populated by
// SetupPackageTestEnv's EnsurePrimordials, well after package var init.
func mdWeaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "LinkWorkOrderReporter", Scope: "any"},
			{OperationType: "RecordWorkOrderResolvedNotice", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// mdSubmitLinkReporter drives one LinkWorkOrderReporter as `actor` with the
// exact declared-read posture the workOrderQueue target's missing_reporter gap
// dispatches under (Reads: the order root and its .report; no OptionalReads).
// Class is LEFT EMPTY so the Processor's operationType→class reverse index is
// what resolves the handler (the target's Class pin names the same workOrder
// DDL; the unpinned path is the stricter one to prove).
func mdSubmitLinkReporter(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	actor, label, workOrderKey string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"workOrderKey": workOrderKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "LinkWorkOrderReporter",
		Actor:         actor,
		SubmittedAt:   "2026-07-21T10:00:00Z",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads: []string{workOrderKey, workOrderKey + ".report"},
		},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// mdSeedLegacyOrder seeds a work order in the shape an order minted before
// ReportIssue wrote the reporter link carries: root + .report stamping the
// reporter + locatedAt, and NO reportedBy link. Returns the order key.
func mdSeedLegacyOrder(t *testing.T, ctx context.Context, conn *substrate.Conn, woID, reporter string, alive bool) string {
	t.Helper()
	woKey := "vtx.workorder." + woID
	put := func(key string, doc map[string]any) {
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	put(woKey, map[string]any{"class": "workorder", "isDeleted": !alive, "data": map[string]any{}})
	report := map[string]any{"summary": "Boiler cycling", "priority": "urgent", "reportedAt": "2026-07-20T09:00:00Z"}
	if reporter != "" {
		report["reportedBy"] = reporter
	}
	put(woKey+".report", map[string]any{"class": "workOrderReport", "vertexKey": woKey, "localName": "report", "isDeleted": false, "data": report})
	testutil.SeedLink(t, ctx, conn, "lnk.workorder."+woID+".locatedAt.unit."+mdUnitAID, "locatedAt", woKey, mdUnitAKey)
	return woKey
}

// TestReportIssue_MintsTheReporterLinkBesideTheRoot pins the literal link
// key ReportIssue writes in the same batch as the root — source = the
// later-arriving work order, target = the reporting identity, type segment
// `identity` (what an outbound walk rebuilds the far endpoint from) — for
// the staff leg and the resident leg alike, so a fresh order never opens the
// backfill gap.
func TestReportIssue_MintsTheReporterLinkBesideTheRoot(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlinkmint")
	mdSeedWorld(t, ctx, conn)
	mdSeedTenant(t, ctx, conn, true)

	const staffID = "BBMANTWQRKRAHJKMNPQR"
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdlnk00000000000001", mdActorKey, mdUnitAKey, "Kitchen tap is dripping", staffID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue = %v, want Accepted", got)
	}
	link := mdReadDoc(t, ctx, conn, "lnk.workorder."+staffID+".reportedBy.identity."+mdActorID)
	if link["sourceVertex"] != "vtx.workorder."+staffID || link["targetVertex"] != mdActorKey || link["class"] != "reportedBy" {
		t.Errorf("reportedBy link = %+v, want source the work order, target the reporter, class reportedBy", link)
	}

	const residentID = "BBMANTWQRKRBHJKMNPQR"
	outcome, why := mdSubmitSelf(t, ctx, conn, cp, cons, mdSelfEnvelope("mdlnk00000000000002",
		mdTenantKey, mdUnitAKey, "Bathroom fan rattles", residentID,
		[]string{mdResidesInLink(mdTenantKey, mdUnitAKey)},
		&processor.AuthContext{Target: mdTenantKey}))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("resident ReportIssue = %v (%q), want Accepted", outcome, why)
	}
	if !mdKeyLive(ctx, conn, "lnk.workorder."+residentID+".reportedBy.identity."+mdTenantID) {
		t.Error("the resident leg minted no reportedBy link — the reporter relation is written on every leg")
	}
}

// TestLinkWorkOrderReporter_BackfillsALegacyOrder is the ADMIT path: an order
// whose .report stamps a reporter but which carries no reportedBy link (the
// population the missing_reporter gap projects) gains the link, minted from
// the stamp — the identical key ReportIssue would have written.
func TestLinkWorkOrderReporter_BackfillsALegacyOrder(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlinkbackfill")
	mdSeedWorld(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, mdWeaverCapDoc())
	woKey := mdSeedLegacyOrder(t, ctx, conn, "BBMANTWQRKRCHJKMNPQR", mdTechKey, true)

	outcome, reply := mdSubmitLinkReporter(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdlbf00000000000001", woKey)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("LinkWorkOrderReporter = %v (%+v), want Accepted", outcome, reply.Error)
	}
	if reply.PrimaryKey != mdReportedByLink(woKey, mdTechKey) {
		t.Errorf("primaryKey = %q, want the minted link key %q (the op's whole write footprint)", reply.PrimaryKey, mdReportedByLink(woKey, mdTechKey))
	}
	link := mdReadDoc(t, ctx, conn, mdReportedByLink(woKey, mdTechKey))
	if link["sourceVertex"] != woKey || link["targetVertex"] != mdTechKey {
		t.Errorf("backfilled link = %+v, want source the work order, target the stamped reporter", link)
	}
}

// TestLinkWorkOrderReporter_CollisionWithTheMintIsARefusedNoOp pins the
// arbitration between the two writers of one deterministic key: on an order
// ReportIssue linked at mint, the backfill's CreateOnly write collides and
// is refused by the commit path — the link ReportIssue wrote is untouched,
// never rewritten. (The lens never dispatches this shape — the gap is FALSE
// on a linked order — so this is the write's own safety, proven directly.)
func TestLinkWorkOrderReporter_CollisionWithTheMintIsARefusedNoOp(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlinkcollide")
	mdSeedWorld(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, mdWeaverCapDoc())
	const woID = "BBMANTWQRKRDHJKMNPQR"
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdlcl00000000000001", mdActorKey, mdUnitAKey, "Kitchen tap is dripping", woID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue = %v, want Accepted", got)
	}
	woKey := "vtx.workorder." + woID
	before := mdReadDoc(t, ctx, conn, mdReportedByLink(woKey, mdActorKey))

	outcome, _ := mdSubmitLinkReporter(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdlcl00000000000002", woKey)
	if outcome == processor.OutcomeAccepted {
		t.Fatal("a backfill on an already-linked order was accepted; the CreateOnly write must collide")
	}
	mdAssertUntouched(t, before, mdReadDoc(t, ctx, conn, mdReportedByLink(woKey, mdActorKey)), "the colliding backfill")
}

// TestLinkWorkOrderReporter_Refusals: a tombstoned order is UnknownWorkOrder;
// a .report with no reportedBy is InvalidArgument (nothing to link from);
// neither writes a link.
func TestLinkWorkOrderReporter_Refusals(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdlinkrefuse")
	mdSeedWorld(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, mdWeaverCapDoc())

	dead := mdSeedLegacyOrder(t, ctx, conn, "BBMANTWQRKREHJKMNPQR", mdTechKey, false)
	outcome, reply := mdSubmitLinkReporter(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdlrf00000000000001", dead)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownWorkOrder") {
		t.Fatalf("tombstoned order: outcome = %v reply %+v, want Rejected UnknownWorkOrder", outcome, reply.Error)
	}
	if mdKeyLive(ctx, conn, mdReportedByLink(dead, mdTechKey)) {
		t.Error("a refused backfill wrote a link on a dead order")
	}

	unstamped := mdSeedLegacyOrder(t, ctx, conn, "BBMANTWQRKRFHJKMNPQR", "", true)
	outcome, reply = mdSubmitLinkReporter(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdlrf00000000000002", unstamped)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("unstamped report: outcome = %v reply %+v, want Rejected InvalidArgument", outcome, reply.Error)
	}
}
