// maintenance-domain integration tests through the real install + Processor
// pipeline. External test package (maintenancedomain_test) so they exercise
// the public Lattice surface: seed the kernel, install rbac + identity +
// hygiene + orchestration-base + location-domain + maintenance-domain through
// the Processor, then submit the ops and assert the committed Core-KV shape.
//
// The beat these vectors prove is facet-staff-worlds-design.md §6 F5's: a work
// order is raised at a place, queued to a maintenance role, and resolved by
// its claimant under the task's ephemeral grant — with the §10.6 auto-complete
// closing the task on the same commit, which is why no completion op exists.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
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
	maintenancedomain "github.com/operatinggraph/lattice/packages/maintenance-domain"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// The world every vector builds:
//
//	vtx.building.<A>                vtx.building.<B>
//	      ^ containedIn
//	vtx.unit.<A>
//
// mdTech worksAt building A and holds backOfHouse. mdActor is the operator.
const (
	mdActorID  = "BBMANTENANCEACTHJKMN"
	mdActorKey = "vtx.identity." + mdActorID
	mdActorCap = "cap.identity." + mdActorID

	mdTechID  = "BBMANTENANCETECHJKMN"
	mdTechKey = "vtx.identity." + mdTechID
	mdTechCap = "cap.identity." + mdTechID
	// The task path reads a DISJOINT entry (Contract #6 §6.6): FR56 ephemeral
	// grants live in orchestration-base's `cap.ephemeral.<actor>`, never in the
	// `cap.<actor>` doc, which carries roles/permissions/service access only.
	mdTechEphemeralCap = "cap.ephemeral.identity." + mdTechID

	mdBuildingAID = "BBMANTBLDGAHJKMNPQRS"
	mdBuildingBID = "BBMANTBLDGBHJKMNPQRS"
	mdUnitAID     = "BBMANTUNTAHJKMNPQRST"

	mdBuildingAKey = "vtx.building." + mdBuildingAID
	mdBuildingBKey = "vtx.building." + mdBuildingBID
	mdUnitAKey     = "vtx.unit." + mdUnitAID

	mdTaskID  = "BBMANTTASKAHJKMNPQRS"
	mdTaskKey = "vtx.task." + mdTaskID
)

func mdOperatorCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    mdActorCap,
		Actor:                  mdActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ReportIssue", Scope: "any"},
			{OperationType: "ResolveWorkOrder", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// mdTechCapDoc is the maintenance tech's capability: a STANDING ReportIssue
// grant (backOfHouse holds it, confined by the script's workplace guard) and
// NO standing ResolveWorkOrder grant at all. The tech's authority to resolve
// arrives only as the ephemeral grant of the task queued to their role — which
// is exactly what the F5 beat is about, so a vector that seeded a standing
// resolve grant would prove nothing.
func mdTechCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    mdTechCap,
		Actor:                  mdTechKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdTechKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ReportIssue", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse")},
	}
}

// mdTechTaskGrantDoc is what orchestration-base's capabilityEphemeral lens
// projects once a task queued to the tech's role is scopedTo the work order:
// the ONE grant that lets them resolve it, expiring with the task.
func mdTechTaskGrantDoc(workOrderKey string) *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    mdTechEphemeralCap,
		Actor:                  mdTechKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{mdTechKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{{
			Source:        mdTaskKey,
			TaskKey:       mdTaskKey,
			OperationType: "ResolveWorkOrder",
			Target:        workOrderKey,
			ExpiresAt:     now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		}},
	}
}

func setupMaintenanceEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{
		"operator":     bootstrap.RoleOperatorID,
		"frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"),
		"backOfHouse":  pkgmgr.RoleID("identity-domain", "backOfHouse"),
		"provider":     pkgmgr.RoleID("identity-domain", "provider"),
		// orchestration-base's CompleteTask scope=self grant (an assignee
		// completing their own task) reaches every role a task can be
		// assignedTo, consumer included — this package never exercises a
		// consumer-scoped op, so a placeholder id (clinic-domain's
		// clConsumerRoleID idiom) satisfies the installer's GrantsTo
		// NanoID-format validation with no real "consumer" role needed here.
		"consumer": "MDConsumerRoZeHJKMNP",
	}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, locationdomain.Package); err != nil {
		t.Fatalf("install location-domain: %v", err)
	}
	if _, err := inst.Install(ctx, maintenancedomain.Package); err != nil {
		t.Fatalf("install maintenance-domain: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, mdOperatorCapDoc())
	// The operator grant is only half of root — the workplace guard reads the
	// holdsRole LINK to decide whether its caller can prove it.
	testutil.SeedHoldsRole(t, ctx, conn, mdActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func mdPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "md-" + durable,
	})
}

func mdSeedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

// mdAssertUntouched proves a document was not rewritten between two reads.
// lastModifiedByOp is the discriminating field: it carries the per-op tracker
// key, so it differs on every submission that actually commits — unlike
// lastModifiedAt, which every vector here hardcodes to the same submittedAt.
// The presence check is load-bearing: a comparison of two fields the committed
// document does not carry (step8_commit writes key/class/isDeleted/data plus
// the created*/lastModified* triplets, and nothing else) is nil == nil, which
// passes no matter what the op did.
func mdAssertUntouched(t *testing.T, before, after map[string]any, what string) {
	t.Helper()
	stamp, ok := before["lastModifiedByOp"].(string)
	if !ok || stamp == "" {
		t.Fatalf("lastModifiedByOp absent from the committed document — "+
			"the %s no-write assertion would be vacuous", what)
	}
	if got := after["lastModifiedByOp"]; got != stamp {
		t.Errorf("%s rewrote the document (lastModifiedByOp %v → %v); it must be a NO-OP",
			what, stamp, got)
	}
}

func mdReadDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
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

func mdKeyLive(ctx context.Context, conn *substrate.Conn, key string) bool {
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	del, _ := doc["isDeleted"].(bool)
	return !del
}

// mdSeedWorld builds the two-building topology and wires the tech to A only.
func mdSeedWorld(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	mdSeedVertex(t, ctx, conn, mdActorKey, "identity")
	mdSeedVertex(t, ctx, conn, mdTechKey, "identity")
	mdSeedVertex(t, ctx, conn, mdBuildingAKey, "location")
	mdSeedVertex(t, ctx, conn, mdBuildingBKey, "location")
	mdSeedVertex(t, ctx, conn, mdUnitAKey, "location")
	testutil.SeedLink(t, ctx, conn,
		"lnk.unit."+mdUnitAID+".containedIn.building."+mdBuildingAID,
		"containedIn", mdUnitAKey, mdBuildingAKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+mdTechID+".worksAt.building."+mdBuildingAID,
		"worksAt", mdTechKey, mdBuildingAKey)
	testutil.SeedHoldsRole(t, ctx, conn, mdTechKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "backOfHouse"))
}

// mdSeedQueuedTask seeds the FR28 role-queued task orchestration-base's
// CreateTask would mint: open, queuedFor the maintenance role, forOperation
// the ResolveWorkOrder op-meta, scopedTo the work order. The links are not
// what this package's ops read — they are what the §10.6 auto-complete and
// edgeTasks' role-queued Walk read — so seeding them directly keeps the vector
// about maintenance-domain rather than about CreateTask's own routing, which
// orchestration-base already proves.
func mdSeedQueuedTask(t *testing.T, ctx context.Context, conn *substrate.Conn, workOrderKey string) {
	t.Helper()
	doc := map[string]any{
		"class": "task", "isDeleted": false,
		"data": map[string]any{
			"status":    "open",
			"expiresAt": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, mdTaskKey, b); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	roleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse")
	testutil.SeedLink(t, ctx, conn,
		"lnk.task."+mdTaskID+".queuedFor.role."+roleKey[len("vtx.role."):],
		"queuedFor", mdTaskKey, roleKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.task."+mdTaskID+".scopedTo.workorder."+workOrderKey[len("vtx.workorder."):],
		"scopedTo", mdTaskKey, workOrderKey)
}

func mdSubmitReportIssue(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, actorKey, location, summary, workOrderID string) processor.MessageOutcome {
	t.Helper()
	payload := map[string]any{"summary": summary, "location": location}
	if workOrderID != "" {
		payload["workOrderId"] = workOrderID
	}
	b, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ReportIssue",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T09:00:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{location}},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// mdSubmitResolve submits ResolveWorkOrder. taskKey non-empty selects the TASK
// path (authContext {task, target}); empty selects the standing path.
func mdSubmitResolve(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, actorKey, workOrderKey, notes, taskKey string) processor.MessageOutcome {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"workOrderKey": workOrderKey, "notes": notes})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ResolveWorkOrder",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T11:30:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads:         []string{workOrderKey},
			OptionalReads: []string{workOrderKey + ".resolution"},
		},
	}
	if taskKey != "" {
		env.AuthContext = &processor.AuthContext{Task: taskKey, Target: workOrderKey}
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// mdSubmitReportIssueWithReason is mdSubmitReportIssue's sibling for a
// REJECTION vector: it returns the script's own failure message so a test can
// name the guard it means to exercise. A bare outcome check cannot tell the
// location guard from the workplace-confinement guard beside it.
func mdSubmitReportIssueWithReason(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, actorKey, location, summary, workOrderID string) (processor.MessageOutcome, string) {
	t.Helper()
	payload := map[string]any{"summary": summary, "location": location}
	if workOrderID != "" {
		payload["workOrderId"] = workOrderID
	}
	b, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ReportIssue",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T09:00:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{location}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// TestReportIssue_CommitsWorkOrderAtLocation is the producer half: the work
// order, its report aspect, and the locatedAt link land in one batch.
func TestReportIssue_CommitsWorkOrderAtLocation(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdreport")
	mdSeedWorld(t, ctx, conn)

	const woID = "BBMANTWQRKAHJKMNPQRS"
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrep00000000000001",
		mdActorKey, mdUnitAKey, "Kitchen tap is dripping", woID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue = %v, want Accepted", got)
	}

	woKey := "vtx.workorder." + woID
	if !mdKeyLive(ctx, conn, woKey) {
		t.Fatalf("%s: work order not committed", woKey)
	}
	report := mdReadDoc(t, ctx, conn, woKey+".report")
	data, _ := report["data"].(map[string]any)
	if got := data["summary"]; got != "Kitchen tap is dripping" {
		t.Errorf("report.summary = %v, want the reported summary", got)
	}
	if got := data["priority"]; got != "normal" {
		t.Errorf("report.priority = %v, want normal (the default)", got)
	}
	if got := data["reportedBy"]; got != mdActorKey {
		t.Errorf("report.reportedBy = %v, want the trusted submitting actor %s", got, mdActorKey)
	}
	if got := data["reportedAt"]; got != "2026-07-21T09:00:00Z" {
		t.Errorf("report.reportedAt = %v, want canonical-UTC(op.submittedAt)", got)
	}
	if !mdKeyLive(ctx, conn, "lnk.workorder."+woID+".locatedAt.unit."+mdUnitAID) {
		t.Errorf("locatedAt link not committed — the work order has no place")
	}
}

// TestReportIssue_RejectsNonLocationTarget pins the migrated location guard on
// ReportIssue's `location` payload field. The guard reads the KEY's type
// segment — a location vertex's class equals its own key type, and every
// location minted before the taxonomy landed still carries the shared class
// `location`, so no class value names the family and a class check would
// reject one of the two live populations.
//
// The negative vector is a live IDENTITY (a real vertex, not a location) and
// the positive vector is the same op over mdUnitAKey, which mdSeedWorld seeds
// with a concrete key type and the legacy shared class — the exact production
// migration shape. Asserting both is what keeps the rejection attributable to
// the guard rather than to the fixture.
func TestReportIssue_RejectsNonLocationTarget(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdnonloc")
	mdSeedWorld(t, ctx, conn)

	// Positive: a legacy-classed unit is still a location.
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdnlp0000000000001",
		mdActorKey, mdUnitAKey, "Tap drips", "BBMANTWQRKPHJKMNPQRS"); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue at a legacy-classed unit = %v, want Accepted", got)
	}
	// Negative: a live vertex whose type segment is not a location level.
	got, why := mdSubmitReportIssueWithReason(t, ctx, conn, cp, cons, "mdnln0000000000002",
		mdActorKey, mdTechKey, "Tap drips", "BBMANTWQRKNHJKMNPQRS")
	if got != processor.OutcomeRejected {
		t.Fatalf("ReportIssue at a non-location target = %v, want Rejected", got)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
	if mdKeyLive(ctx, conn, "vtx.workorder.BBMANTWQRKNHJKMNPQRS") {
		t.Fatal("a work order was committed against a vertex that is not a location")
	}
}

// TestReportIssue_AcceptsPerTypeClass pins the FORWARD-migration half of the
// class arm, which every other location fixture in this package misses:
// mdSeedWorld seeds the shared pre-taxonomy class everywhere, so narrowing the
// admitted class set back to just that one leaves the whole suite green while
// every location minted AFTER the upgrade silently stops being reportable-at.
//
// A location created by location-domain today carries its own key type as its
// class (CreateLocation writes make_vtx(loc_key, lt, {})), so this is the shape
// production produces from here on.
func TestReportIssue_AcceptsPerTypeClass(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdpertype")
	mdSeedWorld(t, ctx, conn)

	// class == the key's own type segment: what CreateLocation mints now.
	perType := "vtx.unit.BBMANTPERTYPEUNTAHJK"
	mdSeedVertex(t, ctx, conn, perType, "unit")
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdpt00000000000001",
		mdActorKey, perType, "Tap drips", "BBMANTWQRKTHJKMNPQRS"); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue at a per-type-classed unit = %v, want Accepted", got)
	}

	// The KEY arm's own discriminator: an admitted CLASS on a key type that is
	// not a location at all. Every other negative vector here fails both arms
	// at once and so cannot tell the key guard from the class-only guard that
	// preceded it.
	impostor := "vtx.workorder.BBMANTMPSTRWQHJKMNPQ"
	mdSeedVertex(t, ctx, conn, impostor, "location")
	got, why := mdSubmitReportIssueWithReason(t, ctx, conn, cp, cons, "mdpt00000000000002",
		mdActorKey, impostor, "Tap drips", "BBMANTWQRKUHJKMNPQRS")
	if got != processor.OutcomeRejected {
		t.Fatalf("ReportIssue at a non-location target = %v, want Rejected", got)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
	if mdKeyLive(ctx, conn, "vtx.workorder.BBMANTWQRKUHJKMNPQRS") {
		t.Fatal("a work order was committed against a vertex that is not a location")
	}
}

// TestReportIssue_StaffConfinedToWorkplace: the create-path workplace guard.
// The reported location is the subject rather than a resolved target, so the
// guard binds on the payload location — naming a building the caller does not
// worksAt-cover DENIES, it does not escalate.
func TestReportIssue_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())
	cp, cons := mdPipeline(t, ctx, conn, "mdreportstaff")
	mdSeedWorld(t, ctx, conn)

	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrsa00000000000001",
		mdTechKey, mdUnitAKey, "Boiler is cycling", "BBMANTWQRKBHJKMNPQRS"); got != processor.OutcomeAccepted {
		t.Fatalf("staff ReportIssue inside its OWN workplace = %v, want Accepted", got)
	}
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrsb00000000000002",
		mdTechKey, mdBuildingBKey, "Not my building", "BBMANTWQRKCHJKMNPQRS"); got != processor.OutcomeRejected {
		t.Fatalf("staff ReportIssue at ANOTHER building = %v, want Rejected — the multi-org gate", got)
	}
}

// TestResolveWorkOrder_TaskPathResolvesAndAutoCompletes is F5's beat, and the
// reason no completion op exists: the tech holds NO standing ResolveWorkOrder
// grant, resolves under the queued task's ephemeral grant, and the §10.6
// auto-complete closes the task on the same commit.
func TestResolveWorkOrder_TaskPathResolvesAndAutoCompletes(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdresolve")
	mdSeedWorld(t, ctx, conn)

	const woID = "BBMANTWQRKDHJKMNPQRS"
	woKey := "vtx.workorder." + woID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrsv00000000000001",
		mdActorKey, mdUnitAKey, "Boiler in the basement is cycling", woID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue = %v, want Accepted", got)
	}
	mdSeedQueuedTask(t, ctx, conn, woKey)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, mdTechTaskGrantDoc(woKey))

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrsv00000000000002",
		mdTechKey, woKey, "Replaced the pressure valve.", mdTaskKey); got != processor.OutcomeAccepted {
		t.Fatalf("tech ResolveWorkOrder on the task path = %v, want Accepted", got)
	}

	res := mdReadDoc(t, ctx, conn, woKey+".resolution")
	data, _ := res["data"].(map[string]any)
	if got := data["notes"]; got != "Replaced the pressure valve." {
		t.Errorf("resolution.notes = %v, want the submitted notes", got)
	}
	if got := data["resolvedBy"]; got != mdTechKey {
		t.Errorf("resolution.resolvedBy = %v, want the trusted submitting actor %s", got, mdTechKey)
	}

	task := mdReadDoc(t, ctx, conn, mdTaskKey)
	tdata, _ := task["data"].(map[string]any)
	if got := tdata["status"]; got != "complete" {
		t.Errorf("task status = %v, want complete — the §10.6 auto-complete is what closes the task, "+
			"which is why maintenance-domain declares no completion op", got)
	}
}

// TestResolveWorkOrder_IdenticalNotesReplayIsAcceptedNoOp is the OFFLINE
// consumer's vector, not a politeness one: a disconnected device queues the
// resolve and drains on reconnect, and a drain that retries under a fresh
// requestId slips past the Contract #4 tracker. Failing it would lose the
// tech's work at exactly the moment the offline beat pays off.
func TestResolveWorkOrder_IdenticalNotesReplayIsAcceptedNoOp(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdreplay")
	mdSeedWorld(t, ctx, conn)

	const woID = "BBMANTWQRKEHJKMNPQRS"
	woKey := "vtx.workorder." + woID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrpl00000000000001",
		mdActorKey, mdUnitAKey, "Lobby door sticks", woID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue = %v, want Accepted", got)
	}
	const notes = "Planed the frame."
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrpl00000000000002",
		mdActorKey, woKey, notes, ""); got != processor.OutcomeAccepted {
		t.Fatalf("first ResolveWorkOrder = %v, want Accepted", got)
	}
	first := mdReadDoc(t, ctx, conn, woKey+".resolution")

	// A DIFFERENT requestId carrying the IDENTICAL notes — what a drain retry
	// looks like once the tracker's dedup window no longer covers it.
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrpl00000000000003",
		mdActorKey, woKey, notes, ""); got != processor.OutcomeAccepted {
		t.Fatalf("replayed ResolveWorkOrder with identical notes = %v, want Accepted (idempotent no-op)", got)
	}
	again := mdReadDoc(t, ctx, conn, woKey+".resolution")
	mdAssertUntouched(t, first, again, "the identical-notes replay")

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrpl00000000000004",
		mdActorKey, woKey, "Actually I replaced the whole door.", ""); got != processor.OutcomeRejected {
		t.Fatalf("ResolveWorkOrder with DIFFERENT notes = %v, want Rejected — a resolution is terminal", got)
	}
}

// TestResolveWorkOrder_TaskPathReplayStaysIdempotent is the offline drain's
// vector on the path that actually carries it: the tech resolves under the
// task's ephemeral grant, the §10.6 auto-complete closes the task, and the
// reconnecting device re-submits the identical notes under a fresh requestId
// while the grant itself is still live. The resource bind — not a worksAt walk
// — is what has to carry that replay through the authorization the script runs
// ahead of its terminal branch, so the beat needs its own vector rather than
// resting on the standing path's.
func TestResolveWorkOrder_TaskPathReplayStaysIdempotent(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdtaskreplay")
	mdSeedWorld(t, ctx, conn)

	const woID = "BBMANTWQRKMHJKMNPQRS"
	woKey := "vtx.workorder." + woID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrtr00000000000001",
		mdActorKey, mdUnitAKey, "Radiator knocking", woID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue = %v, want Accepted", got)
	}
	mdSeedQueuedTask(t, ctx, conn, woKey)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, mdTechTaskGrantDoc(woKey))

	const notes = "Bled the radiator."
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrtr00000000000002",
		mdTechKey, woKey, notes, mdTaskKey); got != processor.OutcomeAccepted {
		t.Fatalf("tech ResolveWorkOrder on the task path = %v, want Accepted", got)
	}
	first := mdReadDoc(t, ctx, conn, woKey+".resolution")

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrtr00000000000003",
		mdTechKey, woKey, notes, mdTaskKey); got != processor.OutcomeAccepted {
		t.Fatalf("task-path drain retry with identical notes = %v, want Accepted "+
			"(the resource bind carries the replay past the guard)", got)
	}
	mdAssertUntouched(t, first, mdReadDoc(t, ctx, conn, woKey+".resolution"),
		"the task-path drain retry")
}

// TestResolveWorkOrder_StandingPathConfinedToWorkplace: the resolve path's own
// confinement, which — unlike ReportIssue's — resolves the location from the
// TARGET's own topology (the work order's locatedAt link), never a payload
// field. A tech on the task path is exempt because the task's scopedTo grant
// is already the narrower confinement.
func TestResolveWorkOrder_StandingPathConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdresolveconfine")
	mdSeedWorld(t, ctx, conn)

	// A work order at building B — outside the tech's workplace.
	const woID = "BBMANTWQRKFHJKMNPQRS"
	woKey := "vtx.workorder." + woID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrcf00000000000001",
		mdActorKey, mdBuildingBKey, "Lift is out at B", woID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue at building B = %v, want Accepted", got)
	}
	// A standing ResolveWorkOrder grant the tech does not normally hold — the
	// point of the vector is that even WITH one, the workplace guard confines
	// them; the capability plane alone cannot tell staff from root.
	doc := mdTechCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcf00000000000002",
		mdTechKey, woKey, "Not my building.", ""); got != processor.OutcomeRejected {
		t.Fatalf("staff standing ResolveWorkOrder at ANOTHER building = %v, want Rejected", got)
	}
}

// TestResolveWorkOrder_ForeignBuildingCannotDistinguishResolvedFromOpen is the
// READ-oracle vector, not a write one: the terminal branch answers a resolved
// work order (idempotent accept / AlreadyResolved) differently from an open one,
// so a caller the workplace guard denies must be denied IDENTICALLY on both, or
// the pair of outcomes leaks the state — and, by probing notes until the accept
// appears, the resolution text itself — for a building the caller does not work
// at. The positive halves keep the negatives honest: the same tech with the same
// standing grant IS accepted on its OWN building's resolved work order, so the
// rejections track the workplace, not the resolution state.
func TestResolveWorkOrder_ForeignBuildingCannotDistinguishResolvedFromOpen(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdresolveoracle")
	mdSeedWorld(t, ctx, conn)

	const notes = "Rebalanced the lift counterweight."

	// Building B — outside the tech's workplace: one resolved, one still open.
	const foreignResolvedID = "BBMANTWQRKGHJKMNPQRS"
	const foreignOpenID = "BBMANTWQRKHHJKMNPQRS"
	foreignResolved := "vtx.workorder." + foreignResolvedID
	foreignOpen := "vtx.workorder." + foreignOpenID
	// Building A — the tech's own workplace, resolved with the SAME notes.
	const homeResolvedID = "BBMANTWQRKJHJKMNPQRS"
	homeResolved := "vtx.workorder." + homeResolvedID

	for _, seed := range []struct {
		label, loc, id, summary string
	}{
		{"mdrcl00000000000001", mdBuildingBKey, foreignResolvedID, "Lift is out at B"},
		{"mdrcl00000000000002", mdBuildingBKey, foreignOpenID, "Stairwell light out at B"},
		{"mdrcl00000000000003", mdUnitAKey, homeResolvedID, "Lift is out at A"},
	} {
		if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, seed.label,
			mdActorKey, seed.loc, seed.summary, seed.id); got != processor.OutcomeAccepted {
			t.Fatalf("operator ReportIssue %q = %v, want Accepted", seed.summary, got)
		}
	}
	for _, seed := range []struct{ label, key string }{
		{"mdrcl00000000000004", foreignResolved},
		{"mdrcl00000000000005", homeResolved},
	} {
		if got := mdSubmitResolve(t, ctx, conn, cp, cons, seed.label,
			mdActorKey, seed.key, notes, ""); got != processor.OutcomeAccepted {
			t.Fatalf("operator ResolveWorkOrder on %s = %v, want Accepted", seed.key, got)
		}
	}
	before := mdReadDoc(t, ctx, conn, foreignResolved+".resolution")

	// A standing grant the tech does not normally hold — the capability plane
	// alone cannot tell staff from root, which is what makes the script guard
	// the only thing standing between them and building B.
	doc := mdTechCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)

	// The probe: identical notes on a RESOLVED foreign work order (the branch
	// that would otherwise accept as an idempotent no-op) and on an OPEN one.
	// Both must fail the same way.
	resolvedProbe := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcl00000000000006",
		mdTechKey, foreignResolved, notes, "")
	openProbe := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcl00000000000007",
		mdTechKey, foreignOpen, notes, "")
	if resolvedProbe != processor.OutcomeRejected || openProbe != processor.OutcomeRejected {
		t.Fatalf("foreign-building probes: resolved = %v, open = %v — both want Rejected; "+
			"a differing pair is the read oracle (resolved-vs-open at a building the caller does not work at)",
			resolvedProbe, openProbe)
	}
	// Differing notes are denied too. The harness surfaces the outcome and not
	// the reason, so this asserts only that the probe cannot WRITE its way in;
	// the resolved-vs-open pair above is what proves it cannot READ either.
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcl00000000000008",
		mdTechKey, foreignResolved, "Guessing at the notes.", ""); got != processor.OutcomeRejected {
		t.Fatalf("foreign-building probe with differing notes = %v, want Rejected", got)
	}

	after := mdReadDoc(t, ctx, conn, foreignResolved+".resolution")
	mdAssertUntouched(t, before, after, "denied probes")

	// Positive halves — the guard denies on WORKPLACE, not on resolution state:
	// the same tech, same grant, on its own building's already-resolved work
	// order still reaches the idempotent branch, and an open one still resolves.
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcl00000000000009",
		mdTechKey, homeResolved, notes, ""); got != processor.OutcomeAccepted {
		t.Fatalf("tech re-submit on its OWN building's resolved work order = %v, "+
			"want Accepted (the idempotent no-op is still reachable once authorized)", got)
	}
	const homeOpenID = "BBMANTWQRKKHJKMNPQRS"
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrcm00000000000001",
		mdActorKey, mdUnitAKey, "Tap dripping at A", homeOpenID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue at unit A = %v, want Accepted", got)
	}
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrcm00000000000002",
		mdTechKey, "vtx.workorder."+homeOpenID, "Replaced the washer.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("tech ResolveWorkOrder inside its OWN workplace = %v, want Accepted", got)
	}
}
