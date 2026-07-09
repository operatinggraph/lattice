// CreateTask integration tests + install idempotency.
//
// These tests live in an external test package (orchestrationbase_test) so
// they exercise the public Lattice surface a real Capability Package sees:
// seed the kernel, install rbac-domain + identity-domain + orchestration-base
// through the Processor, then submit CreateTask ops and assert outcomes.
package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

const (
	otStaffActorID  = "BBstaffActHJKMNPQRST"
	otStaffActorKey = "vtx.identity." + otStaffActorID
	otStaffCapKey   = "cap.identity." + otStaffActorID
)

// staffCapDoc grants the staff actor CreateTask (scope any).
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    otStaffCapKey,
		Actor:                  otStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{otStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateTask", Scope: "any"},
			{OperationType: "ReAssignTask", Scope: "any"},
			{OperationType: "CompleteTask", Scope: "any"},
			{OperationType: "CancelTask", Scope: "any"},
			{OperationType: "MarkExpired", Scope: "any"},
			{OperationType: "SetAvailability", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// setupOrchEnv seeds the kernel, installs the dependency chain +
// orchestration-base, and seeds the staff cap doc.
func setupOrchEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	installOrchestrationBase(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	return ctx, conn
}

func installOrchestrationBase(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
}

func newTaskPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ot-" + durable,
	})
}

// taskIDFromRequestID predicts the task NanoID the DDL's first nanoid.new()
// mints (deterministic from the requestId, same as the identity DDL).
func taskIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// seedVertex writes a minimal live vertex directly to Core KV.
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

// tombstoneVertex overwrites a vertex doc as logically deleted, simulating an
// out-of-band operator deletion (no in-repo mutator tombstones a task today).
func tombstoneVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone vertex %s: %v", key, err)
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

// TestCreateTask_Success commits the task vertex (status=open) + the three
// links (assignedTo/forOperation/scopedTo) atomically.
func TestCreateTask_Success(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-success")

	assigneeID := "BBassigneeHJKMNPQRST"
	assigneeKey := "vtx.identity." + assigneeID
	opID := "BBapproveBpHJKMNPQRS"
	opKey := "vtx.meta." + opID
	targetID := "BBease4ppHJKMNPQRSTU"
	targetKey := "vtx.leaseapp." + targetID
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	reqID := testutil.GenReqID("CTSuccess0001")
	taskID := taskIDFromRequestID(reqID)
	taskKey := "vtx.task." + taskID
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Task vertex: status=open, expiresAt scalar, NO aspects.
	taskDoc := readDoc(t, ctx, conn, taskKey)
	data, _ := taskDoc["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "open" {
		t.Fatalf("task status = %q, want open", got)
	}
	if got, _ := data["expiresAt"].(string); got != expiresAt {
		t.Fatalf("task expiresAt = %q, want %q", got, expiresAt)
	}
	if _, hasGOT := data["grantedOperationType"]; hasGOT {
		t.Fatalf("task root data must NOT carry grantedOperationType (anti-pattern)")
	}

	// The three links (task = source, other vertex = target).
	assignedLnk := "lnk.task." + taskID + ".assignedTo.identity." + assigneeID
	foropLnk := "lnk.task." + taskID + ".forOperation.meta." + opID
	scopedLnk := "lnk.task." + taskID + ".scopedTo.leaseapp." + targetID
	for name, lnk := range map[string]string{"assignedTo": assignedLnk, "forOperation": foropLnk, "scopedTo": scopedLnk} {
		doc := readDoc(t, ctx, conn, lnk)
		if got, _ := doc["sourceVertex"].(string); got != taskKey {
			t.Fatalf("%s link sourceVertex = %q, want %q (task is source)", name, got, taskKey)
		}
	}
	if got, _ := readDoc(t, ctx, conn, assignedLnk)["targetVertex"].(string); got != assigneeKey {
		t.Fatalf("assignedTo targetVertex = %q, want %q", got, assigneeKey)
	}
}

// TestCreateTask_AbsentAssignee_Rejected proves the no-orphan invariant: a
// task pointing at a non-existent identity is never committed (ScriptError).
func TestCreateTask_AbsentAssignee_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-absent")

	missingAssignee := "vtx.identity.BBmissingHJKMNPQRSTU"
	opKey := "vtx.meta.BBapproveBpHJKMNPQRS"
	targetKey := "vtx.leaseapp.BBease4ppHJKMNPQRSTU"
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTAbsent00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + missingAssignee + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{missingAssignee, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateTask_MissingParam_Rejected: a required param absent → rejected.
func TestCreateTask_MissingParam_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-missing")

	assigneeKey := "vtx.identity.BBassigneeHJKMNPQRST"
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTMissing0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		// Missing forOperation, scopedTo, expiresAt.
		Payload:     json.RawMessage(`{"assignee":"` + assigneeKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestInstall_Idempotent: re-installing orchestration-base is a no-op (the
// installer's presence check + deterministic requestId dedup).
func TestInstall_Idempotent(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}

	res1, err := inst.Install(ctx, orchestrationbase.Package)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(res1.DeclaredKeys) == 0 {
		t.Fatal("first install declared no keys")
	}
	// Second install must succeed (no-op / dedup) and not error.
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("re-install must be idempotent; got: %v", err)
	}

	// Sanity: the dependency-order references resolve (identity-domain installed).
	_ = identitydomain.Package
	_ = rbacdomain.Package
}

// TestCreateTask_DeclaredOptionalReads_CreateThenDedup is the Fire-1 declared
// posture end-to-end (Contract #2 §2.5 / script-read-posture design §3.1):
// the dispatcher declares the DDL's two absence-tolerant kv.Read keys — the
// task dedup key and the assignee's `.availability` aspect — in
// contextHint.optionalReads, exactly as Loom's userTaskOptionalReads and
// Weaver's assignTask plan now do.
//
//  1. First dispatch: BOTH declared keys are absent → hydration records them
//     known-absent (no HydrationMiss), kv.Read branches on the snapshot None,
//     and the task is created.
//  2. Re-dispatch (new requestId, SAME taskId — the Weaver reclaim shape):
//     the dedup key is now PRESENT → hydrated at step 4, kv.Read serves the
//     snapshot doc, and the script no-ops (no duplicate task, links intact).
func TestCreateTask_DeclaredOptionalReads_CreateThenDedup(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-declared-dedup")

	assigneeID := "BBassigneeHJKMNPQRST"
	assigneeKey := "vtx.identity." + assigneeID
	opID := "BBapproveBpHJKMNPQRS"
	opKey := "vtx.meta." + opID
	targetID := "BBease4ppHJKMNPQRSTU"
	targetKey := "vtx.leaseapp." + targetID
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	const suppliedID = "BBdecaredXHJKMNPQRST"
	taskKey := "vtx.task." + suppliedID
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	payload := json.RawMessage(`{"assignee":"` + assigneeKey + `","forOperation":"` + opKey +
		`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `","taskId":"` + suppliedID + `"}`)
	hint := &processor.ContextHint{
		Reads:         []string{assigneeKey, opKey, targetKey},
		OptionalReads: []string{taskKey, assigneeKey + ".availability"},
	}

	dispatch := func(label string) {
		t.Helper()
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CreateTask",
			Actor:         otStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "task",
			Payload:       payload,
			ContextHint:   hint,
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	}

	// 1. Absent declared keys → created, not HydrationMiss.
	dispatch("CTDeclared001")
	taskDoc := readDoc(t, ctx, conn, taskKey)
	data, _ := taskDoc["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "open" {
		t.Fatalf("task status = %q, want open (created off the known-absent snapshot)", got)
	}
	firstEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, taskKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", taskKey, err)
	}

	// 2. Same taskId, new requestId, dedup key now hydrated-present → no-op.
	dispatch("CTDeclared002")
	secondEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, taskKey)
	if err != nil {
		t.Fatalf("KVGet %s after re-dispatch: %v", taskKey, err)
	}
	if secondEntry.Revision != firstEntry.Revision {
		t.Fatalf("re-dispatch touched the task (rev %d → %d); declared dedup must no-op",
			firstEntry.Revision, secondEntry.Revision)
	}
}

// TestCreateTask_DeletedTask_ReviveCommits proves the logical-delete
// create-wedge fix through the real commit path: a task key that reads as
// present-but-isDeleted (an out-of-band operator deletion — no in-repo
// mutator tombstones a task today) must revive via a CAS-guarded update, not
// a blind CreateOnly (which would RevisionConflict forever against the
// still-present key's write history — the pre-fix bug, Contract #10 §10.3).
func TestCreateTask_DeletedTask_ReviveCommits(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-revive")

	assigneeID := "BBassigneeHJKMNPQRST"
	assigneeKey := "vtx.identity." + assigneeID
	opID := "BBapproveBpHJKMNPQRS"
	opKey := "vtx.meta." + opID
	targetID := "BBease4ppHJKMNPQRSTU"
	targetKey := "vtx.leaseapp." + targetID
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	const suppliedID = "BBreviveXXHJKMNPQRST"
	taskKey := "vtx.task." + suppliedID
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	seedVertex(t, ctx, conn, taskKey, "task", map[string]any{"status": "open"})
	tombstoneVertex(t, ctx, conn, taskKey, "task")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTRevive0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `","taskId":"` + suppliedID + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{assigneeKey, opKey, targetKey},
			OptionalReads: []string{taskKey, assigneeKey + ".availability"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	taskDoc := readDoc(t, ctx, conn, taskKey)
	if got, _ := taskDoc["isDeleted"].(bool); got {
		t.Fatal("revived task must read isDeleted=false")
	}
	data, _ := taskDoc["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "open" {
		t.Fatalf("revived task status = %q, want open", got)
	}
	if got, _ := data["expiresAt"].(string); got != expiresAt {
		t.Fatalf("revived task expiresAt = %q, want %q", got, expiresAt)
	}
}
