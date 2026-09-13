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
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	augur "github.com/operatinggraph/lattice/packages/augur"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

const (
	apStaffActorID  = "BBstaffActHJKMNPQRST"
	apStaffActorKey = "vtx.identity." + apStaffActorID
	apStaffCapKey   = "cap.identity." + apStaffActorID
)

// staffCapDoc grants the staff actor the full Augur op set
// (CreateAugurReasoningClaim + RecordProposal + ReviewProposal, scope any) — the
// Weaver directOp + bridge replyOp + human-reviewer authority, modeled here as an
// operator-equivalent staff actor.
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
			{OperationType: "ReviewProposal", Scope: "any"},
			{OperationType: "RecordProposalDispatch", Scope: "any"},
			{OperationType: "RecordPromotionProposal", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// weaverCapDoc grants Weaver's primordial dispatch actor
// CreateAugurReasoningClaim — the directOp whose script pins op.actor to
// `primordialActor["weaver"]`. Read through a func, not a package var:
// bootstrap's primordial globals are populated by SetupPackageTestEnv's
// EnsurePrimordials, well after package var initialization.
//
// staffCapDoc keeps its own CreateAugurReasoningClaim + RecordPromotionProposal
// grants deliberately — an operator-role holder that is NOT Weaver is exactly the
// forged-dispatch vector the guard rejects, and the negative tests submit as
// apStaffActorKey to prove the refusal comes from the actor check rather than
// from a missing grant.
func weaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAugurReasoningClaim", Scope: "any"},
			{OperationType: "RecordPromotionProposal", Scope: "any"},
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
	testutil.SeedCapDoc(t, ctx, conn, weaverCapDoc())
	return ctx, conn
}

func installPkg(t *testing.T, ctx context.Context, conn *substrate.Conn, pkg pkgmgr.Definition) {
	t.Helper()
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
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
// the top-level payload (Option F — no nested params object). Weaver's own
// dispatcher (augurEscalation, internal/weaver/strategist.go) declares the
// no-orphan candidate + target endpoints as belt-and-suspenders Reads
// alongside the instanceOp's own kv.Read alive checks, so this fixture mirrors
// that Reads set rather than leaving it undeclared.
//
// The actor is Weaver's primordial key because the script pins op.actor to
// `primordialActor["weaver"]`: any other actor is denied before the branch's
// payload-shape checks run, which would make every scenario below pass on the
// guard rather than on what it means to assert.
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
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{entityKey, targetKey}},
	}
}

// recordReplyEnv builds the bridge replyOp — the {externalRef, status, result}
// shape the bridge actually posts. The bridge's own dispatcher
// (internal/bridge/dispatch.go's replyOpReads) declares the claim's .gap
// aspect it reconstructs the trusted context from, so this fixture mirrors
// that Reads set.
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
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.augurproposal." + handle + ".gap"}},
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
	hForged  = "BBaugurFrgdHJKMNPQRS"
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

// TestAugur_ClaimStoresModelOverride proves the weaver-exhausted-escalation-
// and-model wiring's second half: CreateAugurReasoningClaim's optional "model"
// param (Weaver's augur.model override) is stored on the .gap aspect alongside
// the rest of the TRUSTED escalation context — so a model-backed adapter has it
// available. Omitted entirely, it stores as "" (never fails, never invents a
// value) — the adapter applies its own default.
func TestAugur_ClaimStoresModelOverride(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-model")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	const handle = "BBaugurMdgtHJKMNPQRS"
	payload := map[string]any{
		"instanceKey": handle,
		"adapter":     "augur",
		"replyOp":     "RecordProposal",
		"targetId":    targetKey,
		"entityId":    entityKey,
		"gapColumn":   "missing_bgcheck",
		"trigger":     "exhausted",
		"model":       "claude-sonnet-4-6",
	}
	b, _ := json.Marshal(payload)
	claim := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("APClaimModel"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAugurReasoningClaim",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{entityKey, targetKey}},
	}
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	proposalKey := "vtx.augurproposal." + handle
	gap := readDoc(t, ctx, conn, proposalKey+".gap")
	gd, _ := gap["data"].(map[string]any)
	if got, _ := gd["model"].(string); got != "claude-sonnet-4-6" {
		t.Fatalf(".gap.model = %q, want the threaded override %q", got, "claude-sonnet-4-6")
	}
	if got, _ := gd["trigger"].(string); got != "exhausted" {
		t.Fatalf(".gap.trigger = %q, want exhausted", got)
	}

	// A second claim with NO model param stores "" — never fails, never
	// invents a value; the adapter applies its own default.
	const handleNoModel = "BBaugurNomdHJKMNPQRS"
	claimNoModel := createClaimEnv(testutil.GenReqID("APClaimNoModel"), handleNoModel, targetKey, entityKey)
	testutil.PublishOp(t, conn, claimNoModel)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	gapNoModel := readDoc(t, ctx, conn, "vtx.augurproposal."+handleNoModel+".gap")
	gdNoModel, _ := gapNoModel["data"].(map[string]any)
	if got, _ := gdNoModel["model"].(string); got != "" {
		t.Fatalf(".gap.model with no override = %q, want \"\"", got)
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
// name, so this lands invalid (never dispatchable).
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
// it never fail()s) — a fail()ed op would leave the episode wedged with no
// .review after the bridge had already Ack'd the external event.
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
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
	}
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestAugur_ClaimByNonWeaverOperator_Denied: the primordial actor guard.
// apStaffActorKey holds the operator role AND a Scope:"any"
// CreateAugurReasoningClaim grant, so step 3 authorizes it; only the script's
// `op.actor != primordialActor["weaver"]` check stops it from minting a claim
// over any entity it names and having that forged escalation billed to a
// reasoning model. Identical to the claim the passing tests commit, differing
// in the actor alone.
func TestAugur_ClaimByNonWeaverOperator_Denied(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-forged")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	claim := createClaimEnv(testutil.GenReqID("APClaimForge"), hForged, targetKey, entityKey)
	claim.Actor = apStaffActorKey
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, claim)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a non-Weaver operator's claim: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	// No claim vertex and no .gap aspect: a denied instanceOp leaves the
	// escalation entirely unminted, so nothing downstream (the replyOp, the
	// review lens) can ever see a forged episode.
	proposalKey := "vtx.augurproposal." + hForged
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey); err == nil {
		t.Fatalf("a denied claim op must mint NO proposal vertex")
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey+".gap"); err == nil {
		t.Fatalf("a denied claim op must write NO .gap aspect")
	}
}

// --- ReviewProposal (the human verdict op — design §3.2) ---------------------

// Per-scenario review-episode handles (valid 20-char NanoIDs).
const (
	hRvApprove = "BBaugurApprHJKMNPQRS"
	hRvReject  = "BBaugurRjctHJKMNPQRS"
	hRvNonPend = "BBaugurNpndHJKMNPQRS"
	hRvUnknown = "BBaugurUnknHJKMNPQRS"
	hRvDouble  = "BBaugurDoubHJKMNPQRS"
	hRvRevalFC = "BBaugurRvfcHJKMNPQRS"
)

// Per-scenario RecordProposalDispatch handles.
const (
	hDpDispatched  = "BBaugurDpokHJKMNPQRS"
	hDpInvalid     = "BBaugurDpivHJKMNPQRS"
	hDpNonApproved = "BBaugurDpnaHJKMNPQRS"
	hDpUnknown     = "BBaugurDpukHJKMNPQRS"
	hDpDouble      = "BBaugurDpdbHJKMNPQRS"
	hDpNoReason    = "BBaugurDpnrHJKMNPQRS"
)

// reviewEnv builds the human-verdict op. The operator submits only {externalRef,
// verdict}; the reviewer identity is the TRUSTED actor on the envelope (op.actor)
// and the stamp is op.submittedAt — neither is a payload field (the same
// don't-trust-the-payload-for-identity discipline as RecordProposal's entity split).
func reviewEnv(reqID, handle, verdict string) *processor.OperationEnvelope {
	payload := map[string]any{"externalRef": handle, "verdict": verdict}
	b, _ := json.Marshal(payload)
	proposalKey := "vtx.augurproposal." + handle
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReviewProposal",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		// read-posture class (a) — no production dispatcher yet (hard case 3,
		// script-read-posture-design §13); the test envelope carries the
		// declaration the future UI/dispatcher inherits when wired.
		ContextHint: &processor.ContextHint{Reads: []string{
			proposalKey + ".review", proposalKey + ".proposed",
			proposalKey + ".confidence", proposalKey + ".gap",
		}},
	}
}

// driveReview submits a ReviewProposal and drives it to the wanted outcome.
func driveReview(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, verdict string, want processor.MessageOutcome) {
	t.Helper()
	rv := reviewEnv(testutil.GenReqID("APRev"+tag), handle, verdict)
	testutil.PublishOp(t, conn, rv)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// reviewField reads a string field off vtx.augurproposal.<id>.review.data.
func reviewField(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey, field string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".review")
	data, _ := doc["data"].(map[string]any)
	v, _ := data[field].(string)
	return v
}

// drivePending drives a claim → valid-pending reply and returns the proposal key.
func drivePending(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, targetKey, entityKey string) string {
	t.Helper()
	result := proposalResult("assignTask", 0.82,
		map[string]any{"scopedTo": entityKey, "forOperation": "ApproveLeaseApplication"})
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, tag, handle, targetKey, entityKey, "completed", result)
	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("precondition: review.state = %q, want pending", got)
	}
	return pk
}

// TestAugur_Review_Approve: an operator approves a pending proposal — the verdict
// flips pending → approved, the reviewer (the trusted actor) + stamp are recorded
// on .review, and a reviewedBy link to the actor is created (proposal is source).
func TestAugur_Review_Approve(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-approve")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "appr", hRvApprove, targetKey, entityKey)
	driveReview(t, ctx, conn, cp, cons, "appr", hRvApprove, "approve", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved", got)
	}
	if got := reviewField(t, ctx, conn, pk, "reviewedAt"); got == "" {
		t.Fatalf("reviewedAt must be stamped on review")
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got != "" {
		t.Fatalf("invalidReason = %q, want empty on a clean approve", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got != "" {
		t.Fatalf("dispatchedAt = %q, want empty (dispatch is Fire 2b)", got)
	}
	// reviewedBy link: proposal is the source, the trusted actor is the target.
	lnk := "lnk.augurproposal." + hRvApprove + ".reviewedBy.identity." + apStaffActorID
	link := readDoc(t, ctx, conn, lnk)
	if got, _ := link["sourceVertex"].(string); got != pk {
		t.Fatalf("reviewedBy sourceVertex = %q, want %q (proposal is source)", got, pk)
	}
	if got, _ := link["targetVertex"].(string); got != apStaffActorKey {
		t.Fatalf("reviewedBy targetVertex = %q, want %q (the reviewing actor)", got, apStaffActorKey)
	}
	if ld, _ := link["data"].(map[string]any); ld["verdict"] != "approve" {
		t.Fatalf("reviewedBy.data.verdict = %v, want approve", ld["verdict"])
	}
}

// TestAugur_Review_Reject: an operator rejects a pending proposal — flips to
// rejected with no re-validation (a reject is always permitted).
func TestAugur_Review_Reject(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-reject")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "rjct", hRvReject, targetKey, entityKey)
	driveReview(t, ctx, conn, cp, cons, "rjct", hRvReject, "reject", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "rejected" {
		t.Fatalf("review.state = %q, want rejected", got)
	}
	if got := reviewField(t, ctx, conn, pk, "reviewedAt"); got == "" {
		t.Fatalf("reviewedAt must be stamped on reject")
	}
}

// TestAugur_Review_NonPending_Rejected: only a pending proposal is reviewable.
// Reviewing an invalid proposal is rejected (InvalidReviewTransition) and the
// stored verdict is unchanged — the terminal-state guard.
func TestAugur_Review_NonPending_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-nonpend")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	// A bad-action reply lands review.state=invalid.
	result := proposalResult("DROP TABLE", 0.99, nil)
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "npnd", hRvNonPend, targetKey, entityKey, "completed", result)
	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("precondition: review.state = %q, want invalid", got)
	}

	driveReview(t, ctx, conn, cp, cons, "npnd", hRvNonPend, "approve", processor.OutcomeRejected)
	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (unchanged — an invalid proposal cannot be reviewed)", got)
	}
}

// TestAugur_Review_UnknownProposal_Rejected: reviewing a handle with no recorded
// proposal is rejected (a verdict can never fabricate a proposal).
func TestAugur_Review_UnknownProposal_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-unknown")
	seedEscalation(t, ctx, conn)

	driveReview(t, ctx, conn, cp, cons, "unkn", hRvUnknown, "approve", processor.OutcomeRejected)
}

// TestAugur_Review_DoubleReview_Rejected: a proposal is reviewed once. A second
// genuine review (distinct requestId) finds the proposal already approved (not
// pending) and is rejected — the pending-only guard prevents a re-review.
func TestAugur_Review_DoubleReview_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-double")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "dbl1", hRvDouble, targetKey, entityKey)
	driveReview(t, ctx, conn, cp, cons, "dbl1", hRvDouble, "approve", processor.OutcomeAccepted)
	driveReview(t, ctx, conn, cp, cons, "dbl2", hRvDouble, "reject", processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (the second review must not overwrite)", got)
	}
}

// TestAugur_Review_Approve_RevalidationFailCloses: the §3.2 approval re-validation
// is defense-in-depth — if a pending proposal's stored .proposed no longer passes
// the §5 boundary, approve fail-closes to invalid (never approved/dispatchable).
// The adversarial precondition (a pending proposal carrying an out-of-vocabulary
// action) cannot arise through the validated record path, so the test forces it by
// overwriting the stored .proposed aspect before approval.
func TestAugur_Review_Approve_RevalidationFailCloses(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-revalfc")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "rvfc", hRvRevalFC, targetKey, entityKey)
	// Force the precondition: tamper the stored remediation to an out-of-vocabulary
	// action while the verdict is still pending.
	seedVertex(t, ctx, conn, pk+".proposed", "augur.proposed",
		map[string]any{"action": "DROP TABLE", "params": map[string]any{}})

	driveReview(t, ctx, conn, cp, cons, "rvfc", hRvRevalFC, "approve", processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (approval re-validation must fail-close)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got == "" {
		t.Fatalf("invalidReason must explain the re-validation failure")
	}
}

// --- RecordProposalDispatch (the Weaver-submitted flip — design Fire 2b §3.3) -

// dispatchEnv builds the Weaver-submitted flip op. reason is omitted from the
// payload when empty (the invalid-outcome-only field).
func dispatchEnv(reqID, handle, outcome, reason string) *processor.OperationEnvelope {
	payload := map[string]any{"externalRef": handle, "outcome": outcome}
	if reason != "" {
		payload["reason"] = reason
	}
	b, _ := json.Marshal(payload)
	proposalKey := "vtx.augurproposal." + handle
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordProposalDispatch",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		// The flip reads the verdict it guards on, the recorded plan it counts
		// legs against, and the trigger that refuses a promotion — read-posture
		// class (a), the same set Weaver's recordDispatchOutcomePlan declares
		// (internal/weaver/augur_dispatch.go).
		ContextHint: &processor.ContextHint{Reads: []string{
			proposalKey, proposalKey + ".review", proposalKey + ".proposed", proposalKey + ".gap",
		}},
	}
}

// driveDispatch submits a RecordProposalDispatch and drives it to the wanted
// outcome.
func driveDispatch(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, outcome, reason string, want processor.MessageOutcome) {
	t.Helper()
	dp := dispatchEnv(testutil.GenReqID("APDisp"+tag), handle, outcome, reason)
	testutil.PublishOp(t, conn, dp)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// driveApproved drives a claim → pending reply → approve and returns the
// proposal key, the precondition every dispatch test needs.
func driveApproved(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, targetKey, entityKey string) string {
	t.Helper()
	pk := drivePending(t, ctx, conn, cp, cons, tag, handle, targetKey, entityKey)
	driveReview(t, ctx, conn, cp, cons, tag, handle, "approve", processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}
	return pk
}

// TestAugur_Dispatch_Dispatched: Weaver flips an approved proposal to
// dispatched — dispatchedAt is stamped, invalidReason stays empty.
func TestAugur_Dispatch_Dispatched(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-ok")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := driveApproved(t, ctx, conn, cp, cons, "dpok", hDpDispatched, targetKey, entityKey)
	driveDispatch(t, ctx, conn, cp, cons, "dpok", hDpDispatched, "dispatched", "", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "dispatched" {
		t.Fatalf("review.state = %q, want dispatched", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got == "" {
		t.Fatal("dispatchedAt must be stamped on a dispatched outcome")
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got != "" {
		t.Fatalf("invalidReason = %q, want empty on a clean dispatch", got)
	}
}

// TestAugur_Dispatch_Invalid: Weaver flips an approved proposal to invalid
// (the dispatch-time §5 re-validation failed) — invalidReason is recorded,
// dispatchedAt stays empty (nothing fired).
func TestAugur_Dispatch_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-inv")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := driveApproved(t, ctx, conn, cp, cons, "dpinv", hDpInvalid, targetKey, entityKey)
	driveDispatch(t, ctx, conn, cp, cons, "dpinv", hDpInvalid, "invalid", "operation no longer resolves", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got != "operation no longer resolves" {
		t.Fatalf("invalidReason = %q, want the dispatch-time reason", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got != "" {
		t.Fatalf("dispatchedAt = %q, want empty (nothing was dispatched)", got)
	}
}

// TestAugur_Dispatch_NonApproved_Rejected: only an approved proposal can be
// dispatched — a pending proposal rejects InvalidDispatchTransition.
func TestAugur_Dispatch_NonApproved_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-npnd")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "dpnpnd", hDpNonApproved, targetKey, entityKey)
	driveDispatch(t, ctx, conn, cp, cons, "dpnpnd", hDpNonApproved, "dispatched", "", processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("review.state = %q, want pending (unchanged — a pending proposal cannot be dispatched)", got)
	}
}

// TestAugur_Dispatch_UnknownProposal_Rejected: dispatching a handle with no
// recorded proposal is rejected (a flip can never fabricate a proposal).
func TestAugur_Dispatch_UnknownProposal_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-unk")
	seedEscalation(t, ctx, conn)

	driveDispatch(t, ctx, conn, cp, cons, "dpunk", hDpUnknown, "dispatched", "", processor.OutcomeRejected)
}

// TestAugur_Dispatch_DoubleDispatch_Rejected: a proposal is dispatched once. A
// second genuine flip attempt (distinct requestId) finds the proposal already
// dispatched (not approved) and is rejected — the approved-only guard prevents
// a re-flip.
func TestAugur_Dispatch_DoubleDispatch_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-dbl")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := driveApproved(t, ctx, conn, cp, cons, "dpdbl", hDpDouble, targetKey, entityKey)
	driveDispatch(t, ctx, conn, cp, cons, "dpdbl1", hDpDouble, "dispatched", "", processor.OutcomeAccepted)
	driveDispatch(t, ctx, conn, cp, cons, "dpdbl2", hDpDouble, "invalid", "should never land", processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "dispatched" {
		t.Fatalf("review.state = %q, want dispatched (the second flip must not overwrite)", got)
	}
}

// TestAugur_Dispatch_InvalidOutcomeRequiresReason_Rejected: an outcome=invalid
// flip with no reason is rejected — the invalid verdict must always be
// auditable.
func TestAugur_Dispatch_InvalidOutcomeRequiresReason_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-dp-noreason")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := driveApproved(t, ctx, conn, cp, cons, "dpnr", hDpNoReason, targetKey, entityKey)
	driveDispatch(t, ctx, conn, cp, cons, "dpnr", hDpNoReason, "invalid", "", processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (unchanged — the malformed flip must not land)", got)
	}
}

// --- Plan-shaped proposals + per-leg dispatch -------------------------------

// Per-scenario plan-episode handles (valid 20-char NanoIDs).
const (
	hPnPending = "BBaugurPnokHJKMNPQRS"
	hPnScope   = "BBaugurPnscHJKMNPQRS"
	hPnTooMany = "BBaugurPnmxHJKMNPQRS"
	hPnReval   = "BBaugurPnrvHJKMNPQRS"
	hPnChain   = "BBaugurPnchHJKMNPQRS"
	hPnStale   = "BBaugurPnstHJKMNPQRS"
	hPnLegacy  = "BBaugurPngcHJKMNPQRS"
	hPnRevLeg  = "BBaugurPnrgHJKMNPQRS"
)

// planResult marshals a PLAN-shaped model proposal — the ordered `steps` list
// the adapter produces instead of a single top-level {action, params}.
func planResult(confidence float64, steps ...map[string]any) string {
	m := map[string]any{
		"steps":      steps,
		"confidence": confidence,
		"rationale":  "an ordered remediation for the stuck gap",
		"model":      "claude-opus-4-8",
		"reasonedAt": "2026-09-13T00:00:00Z",
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// assignStep builds one in-vocabulary, in-scope plan leg.
func assignStep(entityKey, operation string) map[string]any {
	return map[string]any{
		"action": "assignTask",
		"params": map[string]any{"scopedTo": entityKey, "forOperation": operation},
	}
}

// seedAspect writes a full aspect document (vertexKey + localName, the shape the
// Processor's own mutations produce) directly into Core KV — the seam a test uses
// to plant a stored shape the validated write path can no longer produce, such as
// a proposal recorded before `steps` / `leg` existed.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, localName, class string, data map[string]any) {
	t.Helper()
	key := vertexKey + "." + localName
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vertexKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// proposedSteps reads vtx.augurproposal.<id>.proposed.data.steps.
func proposedSteps(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) []any {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".proposed")
	data, _ := doc["data"].(map[string]any)
	steps, _ := data["steps"].([]any)
	return steps
}

// reviewLeg reads vtx.augurproposal.<id>.review.data.leg (a JSON number).
func reviewLeg(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) int {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".review")
	data, _ := doc["data"].(map[string]any)
	n, _ := data["leg"].(float64)
	return int(n)
}

// dispatchLegEnv builds the Weaver flip for one NAMED leg — the shape
// recordDispatchOutcomePlan publishes. dispatchEnv (above) deliberately omits
// `leg` entirely: that is the optional-field-absent vector, the payload a
// dispatcher written before the plan shape sends.
func dispatchLegEnv(reqID, handle, outcome, reason string, leg int) *processor.OperationEnvelope {
	env := dispatchEnv(reqID, handle, outcome, reason)
	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		panic(err)
	}
	payload["leg"] = leg
	b, _ := json.Marshal(payload)
	env.Payload = json.RawMessage(b)
	return env
}

func driveDispatchLeg(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, outcome, reason string, leg int, want processor.MessageOutcome) {
	t.Helper()
	dp := dispatchLegEnv(testutil.GenReqID("APDisp"+tag), handle, outcome, reason, leg)
	testutil.PublishOp(t, conn, dp)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestAugur_Plan_ValidPending: a two-leg plan whose every step is in vocabulary
// and scoped to the escalated candidate records pending, with the ordered steps
// stored, action/params mirroring steps[0], and the leg counter at 0.
func TestAugur_Plan_ValidPending(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-ok")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	result := planResult(0.78,
		assignStep(entityKey, "ApproveLeaseApplication"),
		assignStep(entityKey, "RecordLeaseDecision"))
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnok", hPnPending, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	steps := proposedSteps(t, ctx, conn, pk)
	if len(steps) != 2 {
		t.Fatalf(".proposed.steps = %d entries, want 2 (%v)", len(steps), steps)
	}
	first, _ := steps[0].(map[string]any)
	second, _ := steps[1].(map[string]any)
	firstParams, _ := first["params"].(map[string]any)
	secondParams, _ := second["params"].(map[string]any)
	if got, _ := firstParams["forOperation"].(string); got != "ApproveLeaseApplication" {
		t.Fatalf("step 1 params = %v, want the first leg's operation", firstParams)
	}
	if got, _ := secondParams["forOperation"].(string); got != "RecordLeaseDecision" {
		t.Fatalf("step 2 params = %v, want the second leg's operation", secondParams)
	}
	// action/params mirror steps[0] — every reader sees one shape.
	proposed := readDoc(t, ctx, conn, pk+".proposed")
	pd, _ := proposed["data"].(map[string]any)
	if got, _ := pd["action"].(string); got != "assignTask" {
		t.Fatalf(".proposed.action = %q, want the mirrored steps[0].action", got)
	}
	mirrored, _ := pd["params"].(map[string]any)
	if got, _ := mirrored["forOperation"].(string); got != "ApproveLeaseApplication" {
		t.Fatalf(".proposed.params = %v, want the mirrored steps[0].params", mirrored)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 0 {
		t.Fatalf(".review.leg = %d, want 0 (nothing dispatched yet)", got)
	}
}

// TestAugur_Plan_SecondStepScopeEscape_Invalid: the §5 boundary runs PER STEP —
// a plan whose FIRST leg is impeccable and whose second smuggles a foreign
// entity invalidates the WHOLE proposal, and the reason names the failing step
// so a reviewer can find it.
func TestAugur_Plan_SecondStepScopeEscape_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-scope")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	escaping := map[string]any{
		"action": "assignTask",
		"params": map[string]any{
			"scopedTo": entityKey,
			"assignee": "vtx.identity.BBattackerHJKMNPQRS",
		},
	}
	result := planResult(0.9, assignStep(entityKey, "ApproveLeaseApplication"), escaping)
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnsc", hPnScope, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (step 2 escapes scope)", got)
	}
	reason := reviewField(t, ctx, conn, pk, "invalidReason")
	if !strings.Contains(reason, "step 2") {
		t.Fatalf("invalidReason = %q, want it to name step 2", reason)
	}
}

// TestAugur_Plan_TooManySteps_Invalid: a plan longer than the bound is stored
// invalid rather than recorded as an unbounded dispatch chain.
func TestAugur_Plan_TooManySteps_Invalid(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-many")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	steps := make([]map[string]any, 0, 9)
	for i := 0; i < 9; i++ {
		steps = append(steps, assignStep(entityKey, "ApproveLeaseApplication"))
	}
	result := planResult(0.8, steps...)
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnmx", hPnTooMany, targetKey, entityKey, "completed", result)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (9 steps is past the bound)", got)
	}
	if reason := reviewField(t, ctx, conn, pk, "invalidReason"); !strings.Contains(reason, "9 steps") {
		t.Fatalf("invalidReason = %q, want it to name the plan length", reason)
	}
}

// TestAugur_Plan_ApprovalRevalidatesEveryStep: the approval leg re-runs the §5
// boundary over the STORED plan step by step, not just its first leg — a plan
// tampered in its SECOND step fail-closes to invalid on approve.
func TestAugur_Plan_ApprovalRevalidatesEveryStep(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-reval")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	result := planResult(0.78,
		assignStep(entityKey, "ApproveLeaseApplication"),
		assignStep(entityKey, "RecordLeaseDecision"))
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnrv", hPnReval, targetKey, entityKey, "completed", result)
	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("precondition: review.state = %q, want pending", got)
	}
	// Force the precondition the validated record path cannot produce: a stored
	// plan whose SECOND leg is out of vocabulary while the verdict is pending.
	seedVertex(t, ctx, conn, pk+".proposed", "augur.proposed", map[string]any{
		"action": "assignTask",
		"params": map[string]any{"scopedTo": entityKey, "forOperation": "ApproveLeaseApplication"},
		"steps": []any{
			map[string]any{"action": "assignTask", "params": map[string]any{"scopedTo": entityKey, "forOperation": "ApproveLeaseApplication"}},
			map[string]any{"action": "DROP TABLE", "params": map[string]any{"scopedTo": entityKey}},
		},
	})

	driveReview(t, ctx, conn, cp, cons, "pnrv", hPnReval, "approve", processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (the approval must re-validate EVERY step)", got)
	}
	if reason := reviewField(t, ctx, conn, pk, "invalidReason"); !strings.Contains(reason, "step 2") {
		t.Fatalf("invalidReason = %q, want it to name step 2", reason)
	}
}

// TestAugur_Plan_DispatchesLegByLeg: the flip advances the plan one leg at a
// time — leg 0 of 2 leaves the proposal approved (dispatchable again, for the
// next leg) with nothing stamped, and leg 1 is the last, which flips it
// dispatched and stamps dispatchedAt. The approve verdict carries the counter
// through untouched.
func TestAugur_Plan_DispatchesLegByLeg(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-chain")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	result := planResult(0.78,
		assignStep(entityKey, "ApproveLeaseApplication"),
		assignStep(entityKey, "RecordLeaseDecision"))
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnch", hPnChain, targetKey, entityKey, "completed", result)
	driveReview(t, ctx, conn, cp, cons, "pnch", hPnChain, "approve", processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 0 {
		t.Fatalf("the verdict must carry the leg counter through: .review.leg = %d, want 0", got)
	}

	driveDispatchLeg(t, ctx, conn, cp, cons, "pnch0", hPnChain, "dispatched", "", 0, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("after leg 0 of 2: review.state = %q, want approved (a leg remains)", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 1 {
		t.Fatalf("after leg 0: .review.leg = %d, want 1", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got != "" {
		t.Fatalf("dispatchedAt = %q, want empty until the LAST leg fires", got)
	}

	driveDispatchLeg(t, ctx, conn, cp, cons, "pnch1", hPnChain, "dispatched", "", 1, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "dispatched" {
		t.Fatalf("after the last leg: review.state = %q, want dispatched", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 2 {
		t.Fatalf("after the last leg: .review.leg = %d, want 2 (the plan's length)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got == "" {
		t.Fatal("dispatchedAt must be stamped once the last leg fires")
	}
}

// TestAugur_Plan_ReviewCarriesTheLegThrough: the verdict decides whether a plan
// may be dispatched, never how far it has got — so the leg counter rides through
// the flip untouched. Planted directly, because the op sequence itself only ever
// reviews a proposal standing at leg 0: an unpreserved counter would be
// invisible there and would silently re-dispatch a completed leg wherever a
// proposal was reviewed mid-plan.
func TestAugur_Plan_ReviewCarriesTheLegThrough(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-rvleg")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	result := planResult(0.78,
		assignStep(entityKey, "ApproveLeaseApplication"),
		assignStep(entityKey, "RecordLeaseDecision"))
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnrl", hPnRevLeg, targetKey, entityKey, "completed", result)
	seedAspect(t, ctx, conn, pk, "review", "augur.review", map[string]any{
		"state": "pending", "invalidReason": "", "reviewedAt": "", "dispatchedAt": "", "leg": 1,
	})

	driveReview(t, ctx, conn, cp, cons, "pnrl", hPnRevLeg, "approve", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 1 {
		t.Fatalf(".review.leg = %d, want 1 (the verdict must not rewind the plan)", got)
	}
}

// TestAugur_Plan_StaleLeg_Rejected: a flip naming a leg the proposal has already
// passed is a stale dispatch — rejected, so a redelivery that survived the
// requestId tracker can never skip a leg of the plan.
func TestAugur_Plan_StaleLeg_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-stale")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	result := planResult(0.78,
		assignStep(entityKey, "ApproveLeaseApplication"),
		assignStep(entityKey, "RecordLeaseDecision"))
	pk := driveClaimThenReply(t, ctx, conn, cp, cons, "pnst", hPnStale, targetKey, entityKey, "completed", result)
	driveReview(t, ctx, conn, cp, cons, "pnst", hPnStale, "approve", processor.OutcomeAccepted)
	driveDispatchLeg(t, ctx, conn, cp, cons, "pnst0", hPnStale, "dispatched", "", 0, processor.OutcomeAccepted)

	// The proposal stands at leg 1; a flip for leg 0 (or leg 2) is stale.
	dp := dispatchLegEnv(testutil.GenReqID("APDisppnstStale"), hPnStale, "dispatched", "", 0)
	_, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, dp)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidDispatchTransition") {
		t.Fatalf("a stale leg must reject InvalidDispatchTransition, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "stale leg") {
		t.Fatalf("the denial must name the stale leg, got %q", reply.Error.Message)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 1 {
		t.Fatalf(".review.leg = %d, want 1 (the stale flip must not advance it)", got)
	}
}

// TestAugur_Plan_LegacyProposalDispatchesAtLegZero is the optional-field-absent
// vector for BOTH new fields at once: a proposal whose stored .proposed carries
// no `steps` and whose .review carries no `leg` — the shape recorded before the
// plan existed — is dispatched by a flip that names no leg at all, and lands
// `dispatched` exactly as it always did.
func TestAugur_Plan_LegacyProposalDispatchesAtLegZero(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-pn-legacy")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := driveApproved(t, ctx, conn, cp, cons, "pngc", hPnLegacy, targetKey, entityKey)
	// Rewrite both aspects into the pre-plan shape.
	seedAspect(t, ctx, conn, pk, "proposed", "augur.proposed", map[string]any{
		"action": "assignTask",
		"params": map[string]any{"scopedTo": entityKey, "forOperation": "ApproveLeaseApplication"},
	})
	seedAspect(t, ctx, conn, pk, "review", "augur.review", map[string]any{
		"state": "approved", "invalidReason": "", "reviewedAt": "2026-09-13T00:00:00Z", "dispatchedAt": "",
	})

	driveDispatch(t, ctx, conn, cp, cons, "pngc", hPnLegacy, "dispatched", "", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "dispatched" {
		t.Fatalf("review.state = %q, want dispatched (a legacy single-leg proposal completes on its one flip)", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 1 {
		t.Fatalf(".review.leg = %d, want 1 (its single leg dispatched)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "dispatchedAt"); got == "" {
		t.Fatal("dispatchedAt must be stamped on the legacy proposal's dispatch")
	}
}

// --- RecordPromotionProposal (Weaver's own engine-authored recommendation) ---

// Per-scenario promotion handles (valid 20-char NanoIDs).
const (
	hPromoOK     = "BBaugurPromHJKMNPQRS"
	hPromoTwice  = "BBaugurPrtwHJKMNPQRS"
	hPromoForged = "BBaugurPrfgHJKMNPQRS"
	hPromoDead   = "BBaugurPrdeHJKMNPQRS"
	hPromoReview = "BBaugurPrrvHJKMNPQRS"
	hPromoDisp   = "BBaugurPrdpHJKMNPQRS"
)

// promotionEnv builds the Weaver-submitted promotion op. Weaver declares the
// target meta vertex as its one read (the no-orphan alive check the script runs)
// and anchors auth on it, so this fixture mirrors that Reads set.
func promotionEnv(reqID, handle, targetKey, gapColumn, actionRef string, window, closed int) *processor.OperationEnvelope {
	payload := map[string]any{
		"handle": handle, "targetId": targetKey, "gapColumn": gapColumn,
		"actionRef": actionRef, "window": window, "closed": closed,
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordPromotionProposal",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{targetKey}},
	}
}

func drivePromotion(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, targetKey string, want processor.MessageOutcome) string {
	t.Helper()
	env := promotionEnv(testutil.GenReqID("APPromo"+tag), handle, targetKey, "missing_approval", "assignApproval", 20, 20)
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return "vtx.augurproposal." + handle
}

// TestAugur_Promotion_MintsPendingRecommendation: Weaver mints the WHOLE
// proposal in one commit — no claim vertex, no reasoning call — already pending
// on the same review surface a model proposal lands on, with the target's own
// meta vertex as both candidate and target.
func TestAugur_Promotion_MintsPendingRecommendation(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-ok")
	targetKey, _ := seedEscalation(t, ctx, conn)

	pk := drivePromotion(t, ctx, conn, cp, cons, "ok", hPromoOK, targetKey, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	if got := reviewLeg(t, ctx, conn, pk); got != 0 {
		t.Fatalf(".review.leg = %d, want 0", got)
	}
	gap := readDoc(t, ctx, conn, pk+".gap")
	gd, _ := gap["data"].(map[string]any)
	if got, _ := gd["trigger"].(string); got != "promotion" {
		t.Fatalf(".gap.trigger = %q, want promotion", got)
	}
	if got, _ := gd["entityId"].(string); got != targetKey {
		t.Fatalf(".gap.entityId = %q, want the target's own meta vertex %q", got, targetKey)
	}
	proposed := readDoc(t, ctx, conn, pk+".proposed")
	pd, _ := proposed["data"].(map[string]any)
	if got, _ := pd["action"].(string); got != "promotePlaybook" {
		t.Fatalf(".proposed.action = %q, want promotePlaybook", got)
	}
	params, _ := pd["params"].(map[string]any)
	if got, _ := params["actionRef"].(string); got != "assignApproval" {
		t.Fatalf(".proposed.params.actionRef = %q, want the recommended ref", got)
	}
	if steps := proposedSteps(t, ctx, conn, pk); len(steps) != 1 {
		t.Fatalf(".proposed.steps = %d entries, want the one normalised leg", len(steps))
	}
	conf := readDoc(t, ctx, conn, pk+".confidence")
	cd, _ := conf["data"].(map[string]any)
	if score, _ := cd["score"].(float64); score != 1.0 {
		t.Fatalf(".confidence.score = %v, want 1.0 (the engine measured it, it did not guess)", cd["score"])
	}
	prov := readDoc(t, ctx, conn, pk+".provenance")
	prd, _ := prov["data"].(map[string]any)
	if got, _ := prd["model"].(string); got != "weaver" {
		t.Fatalf(".provenance.model = %q, want weaver — a reviewer must see this was not reasoned", got)
	}
	rat := readDoc(t, ctx, conn, pk+".rationale")
	rd, _ := rat["data"].(map[string]any)
	text, _ := rd["text"].(string)
	for _, want := range []string{"assignApproval", "missing_approval", "20"} {
		if !strings.Contains(text, want) {
			t.Fatalf(".rationale.text = %q, want it to name %q", text, want)
		}
	}
	// Both links point at the target meta: the recommendation is about the
	// target's playbook, not about any one entity.
	_, targetID, _ := strings.Cut(strings.TrimPrefix(targetKey, "vtx."), ".")
	for name, lnk := range map[string]string{
		"forCandidate": "lnk.augurproposal." + hPromoOK + ".forCandidate.meta." + targetID,
		"forTarget":    "lnk.augurproposal." + hPromoOK + ".forTarget.meta." + targetID,
	} {
		doc := readDoc(t, ctx, conn, lnk)
		if got, _ := doc["targetVertex"].(string); got != targetKey {
			t.Fatalf("%s link targetVertex = %q, want %q", name, got, targetKey)
		}
	}
}

// TestAugur_Promotion_SecondEmissionConflicts is the DURABLE latch: the handle is
// derived from (target, gap, actionRef), so a repeat emission — a restart having
// cleared Weaver's in-memory latch, a second full window — conflicts create-only
// and commits nothing. A second recommendation for the same triple can never
// reach the reviewer.
func TestAugur_Promotion_SecondEmissionConflicts(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-twice")
	targetKey, _ := seedEscalation(t, ctx, conn)

	pk := drivePromotion(t, ctx, conn, cp, cons, "tw1", hPromoTwice, targetKey, processor.OutcomeAccepted)
	// A DISTINCT requestId, so the Contract #4 tracker does not collapse it —
	// the create-only vertex is what must refuse.
	drivePromotion(t, ctx, conn, cp, cons, "tw2", hPromoTwice, targetKey, processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "pending" {
		t.Fatalf("review.state = %q, want pending (the first recommendation stands untouched)", got)
	}
}

// TestAugur_Promotion_ByNonWeaverOperator_Denied: the actor guard. The evidence a
// promotion carries is the engine's own measurement — an operator-role holder
// that is not Weaver must not be able to manufacture it.
func TestAugur_Promotion_ByNonWeaverOperator_Denied(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-forged")
	targetKey, _ := seedEscalation(t, ctx, conn)

	env := promotionEnv(testutil.GenReqID("APPromoForge"), hPromoForged, targetKey, "missing_approval", "assignApproval", 20, 20)
	env.Actor = apStaffActorKey
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a non-Weaver operator's promotion: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "vtx.augurproposal."+hPromoForged); err == nil {
		t.Fatal("a denied promotion must mint NO proposal vertex")
	}
}

// TestAugur_Promotion_AbsentTarget_Rejected: the no-orphan invariant — a
// recommendation about a target that no longer exists is rejected rather than
// minting a proposal whose links dangle.
func TestAugur_Promotion_AbsentTarget_Rejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-dead")
	seedEscalation(t, ctx, conn)

	drivePromotion(t, ctx, conn, cp, cons, "dead", hPromoDead, "vtx.meta.BBgoneTargtHJKMNPQR", processor.OutcomeRejected)
}

// TestAugur_Promotion_ApproveSkipsRevalidation: promotePlaybook is deliberately
// outside the escalation vocabulary, so the §5 re-validation would fail-close
// every approval of a promotion. The trigger — read from the TRUSTED .gap — is
// what exempts it, and an operator's approve lands `approved`.
func TestAugur_Promotion_ApproveSkipsRevalidation(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-review")
	targetKey, _ := seedEscalation(t, ctx, conn)

	pk := drivePromotion(t, ctx, conn, cp, cons, "rv", hPromoReview, targetKey, processor.OutcomeAccepted)
	driveReview(t, ctx, conn, cp, cons, "prrv", hPromoReview, "approve", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (a promotion is ratified, not re-validated)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got != "" {
		t.Fatalf("invalidReason = %q, want empty", got)
	}
}

// TestAugur_Promotion_IsNeverDispatched: the belt-and-suspenders refusal beneath
// the lens exclusion. Even handed a flip directly, an approved promotion refuses
// — there is no remediation in it for the platform to have fired.
func TestAugur_Promotion_IsNeverDispatched(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-disp")
	targetKey, _ := seedEscalation(t, ctx, conn)

	pk := drivePromotion(t, ctx, conn, cp, cons, "dp", hPromoDisp, targetKey, processor.OutcomeAccepted)
	driveReview(t, ctx, conn, cp, cons, "prdp", hPromoDisp, "approve", processor.OutcomeAccepted)

	dp := dispatchEnv(testutil.GenReqID("APPromoDisp"), hPromoDisp, "dispatched", "")
	_, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, dp)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "never dispatched") {
		t.Fatalf("an approved promotion must refuse the dispatch flip, got %+v", reply.Error)
	}
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (the refused flip must not land)", got)
	}
}

// --- ReviewProposal's per-verdict read sets ---------------------------------

// Handles for the per-verdict read-set vectors.
const (
	hRvRejectReads = "BBaugurRvrrHJKMNPQRS"
	hPromoApproveR = "BBaugurPraaHJKMNPQRS"
)

// rejectEnvMinimalReads builds the REJECT op exactly as Loupe's reject
// dispatcher does: {externalRef, verdict} with ONE declared read, the .review
// aspect the pending-only guard needs. A reject asks nothing else — it declines
// the proposal whatever the proposal says — so any read the script makes beyond
// this one is undeclared and the read-drift guard is what says so.
func rejectEnvMinimalReads(reqID, handle string) *processor.OperationEnvelope {
	payload := map[string]any{"externalRef": handle, "verdict": "reject"}
	b, _ := json.Marshal(payload)
	proposalKey := "vtx.augurproposal." + handle
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReviewProposal",
		Actor:         apStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "augurproposal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{proposalKey + ".review"}},
	}
}

// TestAugur_Review_RejectDeclaresOnlyTheReviewAspect pins the reject verdict's
// read set to what its production dispatcher actually declares. The approve arm
// reads .proposed / .confidence / .gap; a reject that fell through the same
// reads would demand a contextHint Loupe's reject button does not send, and the
// op would fail in production while every approve-shaped fixture stayed green.
func TestAugur_Review_RejectDeclaresOnlyTheReviewAspect(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-rv-rejreads")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	pk := drivePending(t, ctx, conn, cp, cons, "rvrr", hRvRejectReads, targetKey, entityKey)

	rv := rejectEnvMinimalReads(testutil.GenReqID("APRevRejReads"), hRvRejectReads)
	testutil.PublishOp(t, conn, rv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "rejected" {
		t.Fatalf("review.state = %q, want rejected", got)
	}
}

// TestAugur_Promotion_ApproveDeclaresTheApproveReadSet is the other half: an
// approve DOES read the trigger, and the approve dispatcher declares it. A
// promotion approved through that four-read envelope lands `approved` — the
// exemption is reached through a declared read, never a live one.
func TestAugur_Promotion_ApproveDeclaresTheApproveReadSet(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-promo-apreads")
	targetKey, _ := seedEscalation(t, ctx, conn)

	pk := drivePromotion(t, ctx, conn, cp, cons, "apr", hPromoApproveR, targetKey, processor.OutcomeAccepted)
	// reviewEnv carries the four reads Loupe's approve dispatcher declares.
	driveReview(t, ctx, conn, cp, cons, "prapr", hPromoApproveR, "approve", processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved", got)
	}
}

// TestAugur_Claim_PromotionTriggerRejected: `trigger` is a payload field, and one
// of its values is authority-bearing — a proposal carrying "promotion" skips the
// §5 re-validation at approval and is excluded from dispatch by the lens. Only
// Weaver's own RecordPromotionProposal may write it, so the escalation op closes
// the vocabulary rather than trusting whatever the caller sends.
func TestAugur_Claim_PromotionTriggerRejected(t *testing.T) {
	ctx, conn := setupAugurEnv(t)
	cp, cons := newProposalPipeline(t, ctx, conn, "ap-trigger")
	targetKey, entityKey := seedEscalation(t, ctx, conn)

	const handle = "BBaugurTrigHJKMNPQRS"
	claim := createClaimEnv(testutil.GenReqID("APClaimTrigger"), handle, targetKey, entityKey)
	var payload map[string]any
	if err := json.Unmarshal(claim.Payload, &payload); err != nil {
		t.Fatalf("unmarshal claim payload: %v", err)
	}
	payload["trigger"] = "promotion"
	b, _ := json.Marshal(payload)
	claim.Payload = json.RawMessage(b)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, claim)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a forged promotion trigger: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "trigger") {
		t.Fatalf("the denial must name the trigger vocabulary, got %+v", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "vtx.augurproposal."+handle+".gap"); err == nil {
		t.Fatal("a rejected claim must write no .gap aspect")
	}
}
