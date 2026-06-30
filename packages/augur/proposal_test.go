// Augur externalTask matched-pair integration tests — the design §5 record-time
// deterministic-validation boundary (the safety core), exercised end-to-end
// through the real Processor across the instanceOp → replyOp flow.
//
// CreateAugurReasoningClaim mints the claim vertex write-ahead with the TRUSTED
// gap context; RecordProposal (the bridge replyOp, payload {externalRef, status,
// result}) reads that trusted context back, decodes the model's structured
// proposal from the opaque result string, and records the verdict. The model
// NEVER supplies the entity it acts on — that identity comes from the claim. The
// tests prove: valid → pending; bad-action / scope-escape / out-of-range
// confidence / refusal → invalid (auditable, never dispatchable); an absent
// candidate is rejected at claim time; a reply with no prior claim is rejected
// (a model reply can never fabricate a proposal).
//
// These tests live in an external test package (augur_test) so they exercise the
// public Lattice surface a real Capability Package sees: seed the kernel, install
// the dependency chain + orchestration-base + augur through the Processor, then
// submit the ops and assert outcomes.
package augur_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	augur "github.com/asolgan/lattice/packages/augur"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

const (
	apStaffActorID  = "BBstaffActHJKMNPQRST"
	apStaffActorKey = "vtx.identity." + apStaffActorID
	apStaffCapKey   = "cap.identity." + apStaffActorID
)

// staffCapDoc grants the staff actor the Augur matched pair
// (CreateAugurReasoningClaim + RecordProposal, scope any) — the Weaver directOp +
// bridge replyOp authority, modeled here as an operator-equivalent staff actor.
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    apStaffCapKey,
		Actor:                  apStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{apStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAugurReasoningClaim", Scope: "any"},
			{OperationType: "RecordProposal", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupAugurEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	installPkg(t, ctx, conn, orchestrationbase.Package)
	installPkg(t, ctx, conn, augur.Package)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	return ctx, conn
}

func installPkg(t *testing.T, ctx context.Context, conn *substrate.Conn, pkg pkgmgr.Definition) {
	t.Helper()
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, pkg); err != nil {
		t.Fatalf("install %s: %v", pkg.Name, err)
	}
}

func newProposalPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ap-" + durable,
	})
}

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

// seedEscalation seeds the two link endpoints (the weaver target meta + the
// candidate entity) and returns their keys.
func seedEscalation(t *testing.T, ctx context.Context, conn *substrate.Conn) (targetKey, entityKey string) {
	t.Helper()
	targetKey = "vtx.meta.BBtargetMtHJKMNPQRST"
	entityKey = "vtx.leaseapp.BBcandidateHJKMNPQRS"
	seedVertex(t, ctx, conn, targetKey, "meta", map[string]any{"canonicalName": "leaseapprovalTarget"})
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{"state": "pending"})
	return targetKey, entityKey
}

// createClaimEnv builds the reasoning instanceOp Weaver submits as a directOp,
// which mints the claim vertex write-ahead with the trusted gap context. Weaver's
// directOp resolves a FLAT params map from the lens row, so every field arrives at
// the top-level payload (Option F — no nested params object). The instanceOp
// validates its link endpoints via kv.Read, so no ContextHint.Reads is needed.
func createClaimEnv(reqID, handle, targetKey, entityKey string) *processor.OperationEnvelope {
	payload := map[string]any{
		"instanceKey": handle,
		"adapter":     "augur",
		"replyOp":     "RecordProposal",
		"targetId":    targetKey,
		"entityId":    entityKey,
		"gapColumn":   "missing_approval",
		"trigger":     "unplannable",
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAugurReasoningClaim",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
	}
}

// recordReplyEnv builds the bridge replyOp — the {externalRef, status, result}
// shape the bridge actually posts (no ContextHint.Reads; the op reads the claim's
// .gap aspect via kv.Read).
func recordReplyEnv(reqID, handle, status, result string) *processor.OperationEnvelope {
	payload := map[string]any{"externalRef": handle, "status": status}
	if result != "" {
		payload["result"] = result
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordProposal",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
	}
}

// proposalResult marshals a model proposal into the JSON string the bridge
// carries verbatim in the replyOp's `result` (the FakeAugur codec produces the
// same shape).
func proposalResult(action string, confidence float64, params map[string]any) string {
	m := map[string]any{
		"action":     action,
		"confidence": confidence,
		"rationale":  "reasoned remediation for the stuck gap",
		"model":      "claude-opus-4-8",
		"reasonedAt": "2026-06-29T00:00:00Z",
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// reviewState reads vtx.augurproposal.<id>.review.data.state.
func reviewState(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".review")
	data, _ := doc["data"].(map[string]any)
	s, _ := data["state"].(string)
	return s
}

// Per-scenario reasoning-episode handles. Each is a valid 20-char NanoID (the
// shape Loom mints for an externalTask instanceKey; Contract #1 keyPattern
// rejects anything else — no 0/O/I/l, exactly 20 chars).
const (
	hPending = "BBaugurPendHJKMNPQRS"
	hBadAct  = "BBaugurBactHJKMNPQRS"
	hEscape  = "BBaugurEscpHJKMNPQRS"
	hConf    = "BBaugurConfHJKMNPQRS"
	hRefusal = "BBaugurRefuHJKMNPQRS"
	hAbsent  = "BBaugurAbsnHJKMNPQRS"
	hNoClaim = "BBaugurNoclHJKMNPQRS"
	hNested  = "BBaugurNestHJKMNPQRS"
	hForeign = "BBaugurFrgnHJKMNPQRS"
	hNoScope = "BBaugurNscpHJKMNPQRS"
	hMalform = "BBaugurMfrmHJKMNPQRS"
)

// driveClaimThenReply runs the full instanceOp → replyOp flow on one pipeline and
// returns the proposal vertex key (vtx.augurproposal.<handle>).
func driveClaimThenReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, targetKey, entityKey, status, result string) string {
	t.Helper()
	claim := createClaimEnv(testutil.GenReqID("APClaim"+tag), handle, targetKey, entityKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reply := recordReplyEnv(testutil.GenReqID("APReply"+tag), handle, status, result)
	testutil.PublishOp(t, conn, reply)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	return "vtx.augurproposal." + handle
}

// TestAugur_ValidPending: a well-formed in-vocabulary proposal whose proposed
// scope matches the escalated candidate is stored review.state=pending
// (dispatchable). The instanceOp commits the .gap aspect + the
// forCandidate/forTarget links (trusted context); the replyOp commits the
// model-derived .proposed/.review aspects.
func TestAugur_ValidPending(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pending")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hPending
	result := proposalResult("assignTask", 0.82,
		map[string]any{"scopedTo": entityKey, "forOperation": "ApproveLeaseApplication"})
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "pend", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	// Root data is minimal (D5).
	root := readDoc(t, ctx, conn, proposalKey)
	if data, _ := root["data"].(map[string]any); len(data) != 0 {
		t.Fatalf("proposal root data must be {} (D5); got %v", data)
	}
	// The .gap aspect carries the TRUSTED escalation context (instanceOp).
	gap := readDoc(t, ctx, conn, proposalKey+".gap")
	gd, _ := gap["data"].(map[string]any)
	if got, _ := gd["gapColumn"].(string); got != "missing_approval" {
		t.Fatalf(".gap.gapColumn = %q, want missing_approval", got)
	}
	if got, _ := gd["entityId"].(string); got != entityKey {
		t.Fatalf(".gap.entityId = %q, want %q", got, entityKey)
	}
	// The .proposed aspect carries the model's remediation (replyOp).
	proposed := readDoc(t, ctx, conn, proposalKey+".proposed")
	pd, _ := proposed["data"].(map[string]any)
	if got, _ := pd["action"].(string); got != "assignTask" {
		t.Fatalf(".proposed.action = %q, want assignTask", got)
	}
	// Both links: proposal is the source.
	forCand := "lnk.augurproposal." + handle + ".forCandidate.leaseapp.BBcandidateHJKMNPQRS"
	forTarget := "lnk.augurproposal." + handle + ".forTarget.meta.BBtargetMtHJKMNPQRST"
	for name, lnk := range map[string]string{"forCandidate": forCand, "forTarget": forTarget} {
		doc := readDoc(t, ctx, conn, lnk)
		if got, _ := doc["sourceVertex"].(string); got != proposalKey {
			t.Fatalf("%s link sourceVertex = %q, want %q (proposal is source)", name, got, proposalKey)
		}
	}
}

// TestAugur_BadAction_Invalid: an action outside the allowed escalation
// vocabulary stores the proposal review.state=invalid (auditable, never
// dispatchable) — the replyOp still ACCEPTS (the proposal is recorded), but the
// verdict is invalid.
func TestAugur_BadAction_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-badaction")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hBadAct
	result := proposalResult("DROP TABLE", 0.99, nil)
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "bact", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid", got)
	}
}

// TestAugur_ScopeEscape_Invalid: a proposed action whose entity-naming param
// references a candidate OTHER than the escalated one (read from the TRUSTED
// claim, not the reply) is stored invalid — the model cannot propose acting on a
// different entity than the gap it reasoned about.
func TestAugur_ScopeEscape_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-escape")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hEscape
	result := proposalResult("directOp", 0.95,
		map[string]any{"scopedTo": "vtx.leaseapp.BBotherEntyHJKMNPQRS"})
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "escp", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (scope escape)", got)
	}
}

// TestAugur_ForeignParamUnderUnlistedKey_Invalid is the 3-layer-review hardening:
// a proposal that scopes its WELL-KNOWN param (scopedTo) to the trusted candidate
// — so the old fixed-allow-list scope check passed it — but smuggles a FOREIGN
// vertex key under a different param name (assignTask's `assignee`, which grants
// authority to that entity on Fire-2 dispatch). The default-deny scope check now
// rejects ANY vtx-shaped value that isn't the escalated candidate, under any param
// name, so this lands invalid (never dispatchable). Before the fix it was pending.
func TestAugur_ForeignParamUnderUnlistedKey_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-foreign")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hForeign
	result := proposalResult("assignTask", 0.9, map[string]any{
		"scopedTo":     entityKey,                          // in-scope (passes the old name-allow-list)
		"assignee":     "vtx.identity.BBattackerHJKMNPQRS", // FOREIGN — grants authority to a third party
		"forOperation": "ApproveLeaseApplication",
	})
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "frgn", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (foreign entity under an unlisted param name)", got)
	}
}

// TestAugur_ScopelessProposal_Invalid: a structurally-valid action that carries NO
// reference to the escalated candidate at all has no bounded target — it cannot be
// made dispatchable, so the default-deny scope check stores it invalid (before the
// fix an empty/scope-less params map coerced to {} and landed pending).
func TestAugur_ScopelessProposal_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-noscope")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hNoScope
	result := proposalResult("assignTask", 0.8, map[string]any{"forOperation": "ApproveLeaseApplication"})
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "nscp", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (proposal does not scope to the candidate)", got)
	}
}

// TestAugur_MalformedCompletedResult_StoredInvalid is the "always stored"
// invariant: a status=completed reply whose result is NOT a decodable JSON object
// (an adapter-wiring fault or a malformed model output) is a definitive verdict —
// the proposal is STILL recorded with review.state=invalid (the replyOp ACCEPTS,
// it never fail()s). Before the fix this fail()ed the op, leaving the episode
// wedged with no .review after the bridge had already Ack'd the external event.
func TestAugur_MalformedCompletedResult_StoredInvalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-malform")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hMalform
	// A completed reply carrying a non-JSON result (not the codec's well-formed output).
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "malf", handle, targetKey, entityKey,
		"completed", "this is not json")

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (malformed completed result stored, not op-rejected)", got)
	}
}

// TestAugur_ConfidenceOutOfRange_Invalid: a confidence outside [0,1] stores the
// proposal invalid.
func TestAugur_ConfidenceOutOfRange_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-conf")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hConf
	result := proposalResult("assignTask", 1.5, map[string]any{"scopedTo": entityKey})
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "conf", handle, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (confidence out of range)", got)
	}
}

// TestAugur_Refusal_Invalid: a modeled refusal (status=failed, no proposal) is a
// definitive verdict — stored invalid (auditable, never dispatchable), NOT a
// crash. The proposal is still recorded (and augur.proposalRecorded emitted).
func TestAugur_Refusal_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-refusal")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	handle := hRefusal
	proposalKey := driveClaimThenReply(t, ctx, conn, cp, cons, "refu", handle, targetKey, entityKey,
		"failed", "augur: model declined to propose (refusal)")

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (refusal)", got)
	}
}

// TestAugur_AbsentCandidate_Rejected: the no-orphan invariant — a claim pointing
// at a non-existent candidate is never minted (the instanceOp is rejected with a
// structured ScriptError, so no proposal vertex exists at all).
func TestAugur_AbsentCandidate_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-absent")
	targetKey, _ := seedEscalation(t, ctx, conn)
	missingEntity := "vtx.leaseapp.BBmissingEnHJKMNPQRS"

	handle := hAbsent
	claim := createClaimEnv(testutil.GenReqID("APAbsent00001"), handle, targetKey, missingEntity)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestAugur_ReplyWithoutClaim_Rejected: the load-bearing safety property — a
// reply for which no claim vertex was minted is REJECTED (a model reply can never
// fabricate a proposal; the trusted gap context must exist write-ahead).
func TestAugur_ReplyWithoutClaim_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-noclaim")
	seedEscalation(t, ctx, conn)

	handle := hNoClaim
	result := proposalResult("assignTask", 0.8, map[string]any{"scopedTo": "vtx.leaseapp.BBcandidateHJKMNPQRS"})
	reply := recordReplyEnv(testutil.GenReqID("APNoClaim0001"), handle, "completed", result)
	testutil.PublishOp(t, conn, reply)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestAugur_NestedParamsRejected pins the Option-F flat-payload contract: Weaver
// dispatches CreateAugurReasoningClaim as a directOp with FLAT top-level params,
// so the legacy nested {"params": {...}} shape (the Loom externalTask passed) is
// no longer accepted — the op rejects with a missing-flat-field ScriptError
// rather than silently reading the nested object. A regression that re-adds
// nested handling would let this through.
func TestAugur_NestedParamsRejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-nested")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	payload := map[string]any{
		"instanceKey": hNested,
		"adapter":     "augur",
		"replyOp":     "RecordProposal",
		"params": map[string]any{ // the legacy nested shape — must be rejected
			"targetId": targetKey, "entityId": entityKey,
			"gapColumn": "missing_approval", "trigger": "unplannable",
		},
	}
	b, _ := json.Marshal(payload)
	claim := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("APNested00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAugurReasoningClaim",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
	}
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
