package weaver

import "testing"

// fixtureActorKey is the dispatching actor every buildPlan call in this
// package's internal tests is given — in production the value Config.ActorKey
// hands the Actuator, which stamps it on the submitted envelope. It is the
// substitution an {actor} enumeration hub resolves to, so a test that asserts
// on a resolved hub asserts against this.
const fixtureActorKey = "vtx.identity.WeaverServiceActor1abc"

// TestBuildPlan_DirectOp_ResolvesReads pins the v1b directOp reads enhancement:
// a directOp gap action templates row.<column> params into the op payload AND
// routes row-templated reads into the dispatched op's ContextHint.Reads, so an
// op that must read its candidate vertex (TombstoneObject) is hydrated. The
// candidate id is already in the lens row (entityKey) — this just routes it.
func TestBuildPlan_DirectOp_ResolvesReads(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:    "directOp",
		Operation: "TombstoneObject",
		Params:    map[string]string{"objectKey": "row.entityKey", "expectedEpoch": "row.linkEpoch"},
		Reads:     []string{"row.entityKey"},
	}
	row := map[string]any{
		"entityKey": "vtx.object.AAobjHJKMNPQRSTUVWX",
		"linkEpoch": int64(7),
	}

	// directOp does not use the registry source, so nil is fine.
	pl, perr := buildPlan(nil, fixtureActorKey, "objectLiveness", "AAobjHJKMNPQRSTUVWX", "missing_owner", ga, row, 99)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.operationType != "TombstoneObject" {
		t.Fatalf("operationType = %q want TombstoneObject", pl.operationType)
	}
	if len(pl.reads) != 1 || pl.reads[0] != "vtx.object.AAobjHJKMNPQRSTUVWX" {
		t.Fatalf("reads = %v want [vtx.object.AAobjHJKMNPQRSTUVWX] (the candidate hydrated for the op)", pl.reads)
	}
	payload := pl.payload("")
	if payload["objectKey"] != "vtx.object.AAobjHJKMNPQRSTUVWX" {
		t.Fatalf("payload objectKey = %v want the templated entityKey", payload["objectKey"])
	}
	if payload["expectedEpoch"] != int64(7) {
		t.Fatalf("payload expectedEpoch = %v (%T) want 7 (the templated linkEpoch)", payload["expectedEpoch"], payload["expectedEpoch"])
	}
	if payload["expectedRevision"] != uint64(99) {
		t.Fatalf("payload expectedRevision = %v want 99 (the row revision Weaver auto-injects)", payload["expectedRevision"])
	}
}

// TestBuildPlan_DirectOp_SetsClass pins the directOp Class thread-through: a
// playbook entry that pins Class (an operationType ambiguous across installed
// vertexType DDLs, e.g. CreateAccount/DebitAccount claimed by multiple ledger
// packages) carries it onto the resolved plan so the dispatched opEnvelope's
// Class can short-circuit the Processor's operationType→class reverse index
// instead of falling closed on MissingClass.
func TestBuildPlan_DirectOp_SetsClass(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:    "directOp",
		Operation: "CreateAccount",
		Class:     "cafeaccount",
	}
	pl, perr := buildPlan(nil, fixtureActorKey, "cafeLedger", "AAentHJKMNPQRSTUVWX", "missing_account", ga, map[string]any{}, 1)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.class != "cafeaccount" {
		t.Fatalf("class = %q want cafeaccount", pl.class)
	}
}

// TestBuildPlan_DirectOp_MissingReadColumn errors when a row-templated read
// references an absent column (a malformed playbook must not fire a read-less op).
func TestBuildPlan_DirectOp_MissingReadColumn(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:    "directOp",
		Operation: "TombstoneObject",
		Reads:     []string{"row.nope"},
	}
	_, perr := buildPlan(nil, fixtureActorKey, "objectLiveness", "e", "missing_owner", ga, map[string]any{"entityKey": "k"}, 1)
	if perr == nil {
		t.Fatalf("expected a planError for a read referencing an absent row column")
	}
}

// TestBuildPlan_DirectOp_ReadsRowColumnAspectSuffix pins the row.<column>.<aspect>
// derived-read form (script-read-posture-design.md §13 hard case 4): a Reads
// entry whose column isn't itself in the row falls back to joining the
// resolved root column's key with the trailing segment, mirroring the
// Starlark idiom `unit + ".listing"` — used by lease-signing's
// missing_listingLeased gap to declare SetListingStatus's unit.listing read.
func TestBuildPlan_DirectOp_ReadsRowColumnAspectSuffix(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:    "directOp",
		Operation: "SetListingStatus",
		Params:    map[string]string{"unit": "row.unitKey", "status": "leased"},
		Reads:     []string{"row.unitKey", "row.unitKey.listing"},
	}
	row := map[string]any{"unitKey": "vtx.unit.AAunitHJKMNPQRSTUV"}
	pl, perr := buildPlan(nil, fixtureActorKey, "leaseApplicationComplete", "AAappHJKMNPQRSTUVWXY", "missing_listingLeased", ga, row, 3)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	want := []string{"vtx.unit.AAunitHJKMNPQRSTUV", "vtx.unit.AAunitHJKMNPQRSTUV.listing"}
	if len(pl.reads) != len(want) || pl.reads[0] != want[0] || pl.reads[1] != want[1] {
		t.Fatalf("reads = %v want %v", pl.reads, want)
	}
}

// TestBuildPlan_DirectOp_ResolvesOptionalReads pins the declaration side of
// Contract #2 §2.5's absence-tolerant read set: a directOp gap's OptionalReads
// resolves through the SAME
// resolver as Reads (a literal passes through verbatim, a row.<column>
// template substitutes from the violation row) and reaches
// plan.optionalReads(claimID) — the closure fire()/planOptionalReads consult
// to build the dispatched op's ContextHint.OptionalReads.
func TestBuildPlan_DirectOp_ResolvesOptionalReads(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:        "directOp",
		Operation:     "TombstoneObject",
		Params:        map[string]string{"objectKey": "row.entityKey"},
		OptionalReads: []string{"vtx.config.residueGuard", "row.entityKey"},
	}
	row := map[string]any{"entityKey": "vtx.object.AAobjHJKMNPQRSTUVWX"}
	pl, perr := buildPlan(nil, fixtureActorKey, "objectLiveness", "AAobjHJKMNPQRSTUVWX", "missing_owner", ga, row, 1)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.optionalReads == nil {
		t.Fatalf("plan declares OptionalReads in the playbook but pl.optionalReads is nil")
	}
	got := pl.optionalReads("anyClaimId")
	want := []string{"vtx.config.residueGuard", "vtx.object.AAobjHJKMNPQRSTUVWX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("optionalReads = %v want %v (literal passed through, row.<column> resolved)", got, want)
	}
}

// TestBuildPlan_DirectOp_NoOptionalReadsLeavesPlanFieldNil proves the omission
// path: a directOp gap that declares no OptionalReads at all leaves
// pl.optionalReads NIL, so nothing downstream allocates or consults a closure
// for a declaration the playbook never made. Nil and an empty slice publish
// the same envelope — the actuator attaches a contextHint only when some list
// is non-empty — so this pins the shape, and every fixture that always
// supplies an optional input pins only the supplied case; this is the vector
// that covers the absent one.
func TestBuildPlan_DirectOp_NoOptionalReadsLeavesPlanFieldNil(t *testing.T) {
	t.Parallel()
	ga := GapAction{
		Action:    "directOp",
		Operation: "TombstoneObject",
		Params:    map[string]string{"objectKey": "row.entityKey"},
	}
	row := map[string]any{"entityKey": "vtx.object.AAobjHJKMNPQRSTUVWX"}
	pl, perr := buildPlan(nil, fixtureActorKey, "objectLiveness", "AAobjHJKMNPQRSTUVWX", "missing_owner", ga, row, 1)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.optionalReads != nil {
		t.Fatalf("plan declares no OptionalReads in the playbook but pl.optionalReads is non-nil: %v", pl.optionalReads("x"))
	}
}

// TestBuildPlan_AssignTask_OptionalReadsMatchPayload pins the Contract #2 §2.5
// optionalReads set an assignTask dispatch declares: exactly the stable task
// dedup key and the assignee's `.availability` routing aspect — and, load-
// bearing, the task key in optionalReads is derived from the SAME claimId-
// seeded id the payload carries as taskId. If the two derivations ever drift,
// the declared dedup read would snapshot the wrong key and the CreateTask
// script's kv.Read would silently fall back to a lazy live GET.
func TestBuildPlan_AssignTask_OptionalReadsMatchPayload(t *testing.T) {
	t.Parallel()
	src := &targetSource{opMetaByType: map[string]string{
		"ApproveLeaseApplication": "vtx.meta.AAopMetaHJKMNPQRSTUV",
	}}
	ga := GapAction{
		Action:    "assignTask",
		Operation: "ApproveLeaseApplication",
		Assignee:  "row.assignee",
		Target:    "row.entityKey",
	}
	row := map[string]any{
		"assignee":  "vtx.identity.AAassignHJKMNPQRSTUV",
		"entityKey": "vtx.leaseApplication.AAleaseHJKMNPQRSTUV",
	}
	pl, perr := buildPlan(src, fixtureActorKey, "leaseApproval", "AAleaseHJKMNPQRSTUV", "missing_approval", ga, row, 7)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	const claimID = "AAcLaimHJKMNPQRSTUVW"
	payload := pl.payload(claimID)
	taskID, _ := payload["taskId"].(string)
	if taskID == "" {
		t.Fatalf("assignTask payload carries no taskId: %v", payload)
	}
	if pl.optionalReads == nil {
		t.Fatalf("assignTask plan declares no optionalReads (dedup key + availability must be declared)")
	}
	got := pl.optionalReads(claimID)
	want := map[string]bool{
		"vtx.task." + taskID:                             true,
		"vtx.identity.AAassignHJKMNPQRSTUV.availability": true,
	}
	if len(got) != len(want) {
		t.Fatalf("optionalReads = %v, want the task dedup key + the assignee availability aspect", got)
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected optionalReads key %q (set: %v)", k, got)
		}
	}
}

// TestBuildPlan_AssignTask_QueueArm pins the Queue arm of assignTask: a gap
// naming a role queue instead of an assignee dispatches CreateTask with
// `queue` in the assignee's place, the required reads are exactly
// [queue, forOperation, target] (the three keys the CreateTask script
// vertex_alive-checks on its queue branch), and the optionalReads carry the
// stable task dedup key ALONE — the `.availability` routing aspect is read on
// the script's assignee branch only, so declaring it here would be a phantom
// read of a key the op never touches. The rest of the plan is the assignee
// arm's: authTarget = the task target, taskId claimId-seeded and stable across
// two claims of the same episode, expiresAt carried, expectedRevision carried.
func TestBuildPlan_AssignTask_QueueArm(t *testing.T) {
	t.Parallel()
	const opMeta = "vtx.meta.AAopMetaHJKMNPQRSTUV"
	const roleKey = "vtx.role.AAbackHJKMNPQRSTUVWX"
	const orderKey = "vtx.workorder.AAorderHJKMNPQRSTUVW"
	src := &targetSource{opMetaByType: map[string]string{"ResolveWorkOrder": opMeta}}
	ga := GapAction{
		Action:    "assignTask",
		Operation: "ResolveWorkOrder",
		Queue:     roleKey,
		Target:    "row.entityKey",
	}
	row := map[string]any{"entityKey": orderKey}
	pl, perr := buildPlan(src, fixtureActorKey, "workOrderQueue", "AAorderHJKMNPQRSTUVW", "missing_task", ga, row, 11)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.operationType != opCreateTask {
		t.Fatalf("operationType = %q, want %q", pl.operationType, opCreateTask)
	}
	if pl.authTarget != orderKey {
		t.Fatalf("authTarget = %q, want the task target %q", pl.authTarget, orderKey)
	}
	wantReads := []string{roleKey, opMeta, orderKey}
	if len(pl.reads) != len(wantReads) {
		t.Fatalf("reads = %v, want exactly [queue, forOperation, target] = %v", pl.reads, wantReads)
	}
	for i := range wantReads {
		if pl.reads[i] != wantReads[i] {
			t.Fatalf("reads[%d] = %q, want %q (full: %v)", i, pl.reads[i], wantReads[i], pl.reads)
		}
	}

	const claimA = "AAcLaimHJKMNPQRSTUVW"
	payload := pl.payload(claimA)
	if got := payload["queue"]; got != roleKey {
		t.Fatalf("payload queue = %v, want %q", got, roleKey)
	}
	if _, present := payload["assignee"]; present {
		t.Fatalf("a queue-arm payload must carry no assignee: %v", payload)
	}
	if got := payload["forOperation"]; got != opMeta {
		t.Fatalf("payload forOperation = %v, want %q", got, opMeta)
	}
	if got := payload["scopedTo"]; got != orderKey {
		t.Fatalf("payload scopedTo = %v, want %q", got, orderKey)
	}
	if got, _ := payload["expiresAt"].(string); got == "" {
		t.Fatalf("payload carries no expiresAt: %v", payload)
	}
	if got := payload["expectedRevision"]; got != uint64(11) {
		t.Fatalf("payload expectedRevision = %v (%T), want 11", got, got)
	}
	taskID, _ := payload["taskId"].(string)
	if taskID == "" {
		t.Fatalf("queue-arm payload carries no taskId: %v", payload)
	}
	if taskID != deriveStableTaskID("workOrderQueue", "AAorderHJKMNPQRSTUVW", "missing_task", claimA) {
		t.Fatalf("taskId %q is not the claimId-seeded stable id", taskID)
	}
	if again, _ := pl.payload(claimA)["taskId"].(string); again != taskID {
		t.Fatalf("taskId drifted across two claims of one episode: %q then %q", taskID, again)
	}
	if other, _ := pl.payload("AAcLaimHJKMNPQRSTUVX")["taskId"].(string); other == taskID {
		t.Fatalf("taskId must be claimId-seeded, but a different claim derived the same id %q", taskID)
	}

	if pl.optionalReads == nil {
		t.Fatalf("queue-arm plan declares no optionalReads (the stable task dedup key must be declared)")
	}
	got := pl.optionalReads(claimA)
	if len(got) != 1 || got[0] != "vtx.task."+taskID {
		t.Fatalf("optionalReads = %v, want exactly the stable task dedup key %q and no availability aspect", got, "vtx.task."+taskID)
	}
}

// TestBuildPlan_AssignTask_QueueArm_ResolvesRowTemplate pins that the queue
// takes the row.<column> template like every other dispatch-identity field,
// and that a column the row lacks fails the dispatch rather than dispatching
// an empty queue.
func TestBuildPlan_AssignTask_QueueArm_ResolvesRowTemplate(t *testing.T) {
	t.Parallel()
	src := &targetSource{opMetaByType: map[string]string{"ResolveWorkOrder": "vtx.meta.AAopMetaHJKMNPQRSTUV"}}
	ga := GapAction{Action: "assignTask", Operation: "ResolveWorkOrder", Queue: "row.queue", Target: "row.entityKey"}
	row := map[string]any{
		"queue":     "vtx.role.AAbackHJKMNPQRSTUVWX",
		"entityKey": "vtx.workorder.AAorderHJKMNPQRSTUVW",
	}
	pl, perr := buildPlan(src, fixtureActorKey, "workOrderQueue", "AAorderHJKMNPQRSTUVW", "missing_task", ga, row, 1)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if got := pl.payload("AAcLaimHJKMNPQRSTUVW")["queue"]; got != "vtx.role.AAbackHJKMNPQRSTUVWX" {
		t.Fatalf("payload queue = %v, want the row-resolved role key", got)
	}
	if pl.reads[0] != "vtx.role.AAbackHJKMNPQRSTUVWX" {
		t.Fatalf("reads[0] = %q, want the row-resolved role key", pl.reads[0])
	}

	_, perr = buildPlan(src, fixtureActorKey, "workOrderQueue", "AAorderHJKMNPQRSTUVW", "missing_task", ga,
		map[string]any{"entityKey": "vtx.workorder.AAorderHJKMNPQRSTUVW"}, 1)
	if perr == nil {
		t.Fatalf("a row lacking the queue column must fail the dispatch, not dispatch an empty queue")
	}
}
