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
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{
		"operator":     bootstrap.RoleOperatorID,
		"frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"),
		"backOfHouse":  pkgmgr.RoleID("identity-domain", "backOfHouse"),
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
// what this package's ops read — they are what the §10.6 auto-complete and the
// edgeTasksQueued projection read — so seeding them directly keeps the vector
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
	if first["revision"] != again["revision"] {
		t.Errorf("replay rewrote the resolution (revision %v → %v); it must be a NO-OP, not a re-write",
			first["revision"], again["revision"])
	}

	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdrpl00000000000004",
		mdActorKey, woKey, "Actually I replaced the whole door.", ""); got != processor.OutcomeRejected {
		t.Fatalf("ResolveWorkOrder with DIFFERENT notes = %v, want Rejected — a resolution is terminal", got)
	}
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
