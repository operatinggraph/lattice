package weaver

import "testing"

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
	pl, perr := buildPlan(nil, "objectLiveness", "AAobjHJKMNPQRSTUVWX", "missing_owner", ga, row, 99)
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
	pl, perr := buildPlan(nil, "cafeLedger", "AAentHJKMNPQRSTUVWX", "missing_account", ga, map[string]any{}, 1)
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
	_, perr := buildPlan(nil, "objectLiveness", "e", "missing_owner", ga, map[string]any{"entityKey": "k"}, 1)
	if perr == nil {
		t.Fatalf("expected a planError for a read referencing an absent row column")
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
	pl, perr := buildPlan(src, "leaseApproval", "AAleaseHJKMNPQRSTUV", "missing_approval", ga, row, 7)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	const claimID = "AAclaimHJKMNPQRSTUVW"
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
