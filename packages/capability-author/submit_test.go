// SubmitCapabilityProposal integration tests — the HUMAN authoring lane into
// the capability review queue (weaver-target-studio-design.md §6.4), exercised
// end-to-end through the real Processor.
//
// Where the AI lane needs three ops (RequestCapabilityAuthoring →
// CreateAuthoringClaim → RecordCapabilityProposal) because a reasoning call has
// to be dispatched and correlated back, an operator who composed the artifact
// themselves needs one: there is nothing to dispatch, so there is no claim
// handle and no write-ahead request. These tests prove that shortcut costs
// nothing in safety — the same §5 boundary decides pending vs invalid, the
// requester is still the trusted submitting actor, a duplicate proposalId still
// cannot overwrite a live proposal, and the human review gate is entered
// identically.
//
// They live in the external test package (capabilityauthor_test) alongside
// proposal_test.go and reuse its harness (setupCapAuthorEnv, staffCapDoc,
// reviewState, validLensContent).
package capabilityauthor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// One proposal id per scenario, each a valid 20-char bare NanoID over the
// limited alphabet (no I / l / O / 0 — internal/substrate/nanoid.go). An id
// outside that alphabet is refused by the step-6 keyPattern gate before the
// script ever runs, which surfaces only as an unexplained rejection. No claim
// handles here — the whole point of this lane is that there is no claim.
const (
	capIDSubmit          = "CAsubGoodHJKMNPQRSTU"
	capIDSubmitBadKind   = "CAsubKindHJKMNPQRSTU"
	capIDSubmitNoValid   = "CAsubNvfyHJKMNPQRSTU"
	capIDSubmitBadSpec   = "CAsubBspcHJKMNPQRSTU"
	capIDSubmitDuplicate = "CAsubDupeHJKMNPQRSTU"
	capIDSubmitMalformed = "CAsubBadpHJKMNPQRSTU"
	capIDSubmitLabelled  = "CAsubLabeHJKMNPQRSTU"
	capIDSubmitGated     = "CAsubGateHJKMNPQRSTU"
	capIDSubmitNoTarget  = "CAsubNtgtHJKMNPQRSTU"
	capIDSubmitGrant     = "CAsubGrntHJKMNPQRSTU"
	capIDSubmitPkgName   = "CAsubPnrmHJKMNPQRSTU"
	capIDSubmitValWS     = "CAsubVstateHJKMNPQRS"
)

// submitEnv builds a SubmitCapabilityProposal envelope. Unlike recordEnv's
// bridge replyOp shape, every field is an ordinary top-level payload field —
// there is no adapter Detail blob to unwrap. The §5 materializer runs HERE, in
// the caller, exactly as the studio client runs it before enabling Propose.
func submitEnv(t *testing.T, reqID, proposalID, kind string, content json.RawMessage, rationale string) *processor.OperationEnvelope {
	t.Helper()
	report, err := pkgmgr.ValidateCapabilityArtifact(kind, content, fullCypherParser{}, nil, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	validationState := "invalid"
	if report.Valid {
		validationState = "valid"
	}
	validation := map[string]any{"state": validationState}
	if len(report.Errors) > 0 {
		b, _ := json.Marshal(report.Errors)
		validation["report"] = string(b)
	}
	return submitEnvRaw(reqID, map[string]any{
		"proposalId": proposalID,
		"kind":       kind,
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  rationale,
		"validation": validation,
	})
}

// submitEnvRaw builds the envelope from a verbatim payload map, for the
// negative cases that deliberately malform or omit a field.
func submitEnvRaw(reqID string, payload map[string]any) *processor.OperationEnvelope {
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SubmitCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

func aspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, key)
	data, _ := doc["data"].(map[string]any)
	return data
}

// TestCapAuthor_Submit_ValidLens_Pending: one op, no request and no claim,
// mints the whole proposal — every aspect the three-op AI lane writes between
// them — and lands review.state=pending with source='operator'.
func TestCapAuthor_Submit_ValidLens_Pending(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit")

	proposalKey := "vtx.capabilityproposal." + capIDSubmit
	sub := submitEnv(t, testutil.GenReqID("CASubmit"), capIDSubmit, "lens",
		validLensContent(t, "operatorAuthoredProviders"),
		"the on-call roster needs a specialty cut no installed lens projects")
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}

	// Root data is minimal (D5), same as the AI lane's proposal.
	root := readDoc(t, ctx, conn, proposalKey)
	if data, _ := root["data"].(map[string]any); len(data) != 0 {
		t.Fatalf("proposal root data must be {} (D5); got %v", data)
	}

	// The requester is the TRUSTED submitting actor, never a payload field —
	// the no-orphan invariant the AI lane's write-ahead request establishes
	// holds identically here.
	rd := aspectData(t, ctx, conn, proposalKey+".request")
	if got, _ := rd["requesterId"].(string); got != capStaffActorKey {
		t.Fatalf(".request.requesterId = %q, want %q", got, capStaffActorKey)
	}
	// No explicit intent was supplied, so the rationale stands in as the review
	// queue's row label rather than leaving every operator row unlabelled.
	if got, _ := rd["intent"].(string); got != "the on-call roster needs a specialty cut no installed lens projects" {
		t.Fatalf(".request.intent = %q, want the rationale text", got)
	}

	// The requestedBy link: the proposal is the source (Contract #1 §1.1).
	lnk := readDoc(t, ctx, conn, "lnk.capabilityproposal."+capIDSubmit+".requestedBy.identity."+capStaffActorID)
	if got, _ := lnk["sourceVertex"].(string); got != proposalKey {
		t.Fatalf("requestedBy link sourceVertex = %q, want %q (proposal is source)", got, proposalKey)
	}

	ad := aspectData(t, ctx, conn, proposalKey+".artifact")
	if got, _ := ad["kind"].(string); got != "lens" {
		t.Fatalf(".artifact.kind = %q, want lens", got)
	}

	// The declared origin the review queue badges from — never inferred from
	// the presence of model-shaped provenance.
	pd := aspectData(t, ctx, conn, proposalKey+".provenance")
	if got, _ := pd["source"].(string); got != "operator" {
		t.Fatalf(".provenance.source = %q, want operator", got)
	}
	if got, _ := pd["model"].(string); got != "" {
		t.Fatalf(".provenance.model = %q, want empty (no model authored this)", got)
	}

	// A human author produces no model confidence: the -1.0 absent-sentinel,
	// not a fabricated score that would sort and band as if it were real.
	cd := aspectData(t, ctx, conn, proposalKey+".confidence")
	score, ok := cd["score"].(float64)
	if !ok || score != -1.0 {
		t.Fatalf(".confidence.score = %v, want -1.0 (absent sentinel)", cd["score"])
	}

	// No claim was minted and none is required — the whole indirection the AI
	// lane needs is absent, not merely unused.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey+".claim"); err == nil {
		t.Fatalf(".claim aspect exists; a directly-submitted proposal must have no authoring claim")
	}
}

// TestCapAuthor_Submit_ExplicitIntent: an explicit intent overrides the
// rationale as the queue's row label, so the studio can label a row with
// something shorter than the operator's full justification.
func TestCapAuthor_Submit_ExplicitIntent(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-intent")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitLabelled
	sub := submitEnv(t, testutil.GenReqID("CASubmit"), capIDSubmitLabelled, "lens",
		validLensContent(t, "labelledLens"), "a long-winded operator justification")
	var payload map[string]any
	if err := json.Unmarshal(sub.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	payload["intent"] = "providers by specialty"
	sub = submitEnvRaw(sub.RequestID, payload)
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rd := aspectData(t, ctx, conn, proposalKey+".request")
	if got, _ := rd["intent"].(string); got != "providers by specialty" {
		t.Fatalf(".request.intent = %q, want the explicit intent", got)
	}
}

// TestCapAuthor_Submit_DisabledKind_Invalid: the §5 kind-enablement half of the
// boundary applies to the human lane exactly as it does to the AI lane — the
// proposal is still RECORDED (auditable), just never dispatchable.
func TestCapAuthor_Submit_DisabledKind_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-kind")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitBadKind
	sub := submitEnvRaw(testutil.GenReqID("CASubmit"), map[string]any{
		"proposalId": capIDSubmitBadKind,
		"kind":       "weaverPlaybook",
		"content":    `{"anything":"at all"}`,
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "a kind this increment does not enable",
		"validation": map[string]any{"state": "valid"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid", got)
	}
	rv := aspectData(t, ctx, conn, proposalKey+".review")
	if got, _ := rv["invalidReason"].(string); got == "" {
		t.Fatalf(".review.invalidReason is empty; an invalid verdict must be auditable")
	}
}

// TestCapAuthor_Submit_MissingValidation_FailCloses: the script has no
// parser/registry access, so an absent §5 verdict is not "unknown, proceed" —
// it fail-closes to invalid, the same posture ReviewCapabilityProposal's
// approve leg takes on a missing fresh verdict.
func TestCapAuthor_Submit_MissingValidation_FailCloses(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-noval")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitNoValid
	sub := submitEnvRaw(testutil.GenReqID("CASubmit"), map[string]any{
		"proposalId": capIDSubmitNoValid,
		"kind":       "lens",
		"content":    string(validLensContent(t, "unvalidatedLens")),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "submitted without running the materializer",
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (no verdict supplied)", got)
	}
	rv := aspectData(t, ctx, conn, proposalKey+".review")
	if got, _ := rv["invalidReason"].(string); got == "" {
		t.Fatalf(".review.invalidReason is empty; want the no-verdict reason")
	}
}

// TestCapAuthor_Submit_MaterializerRejected_Invalid: a real materializer
// rejection (an unparseable lens spec) travels as validation.state != valid and
// records invalid — proving the boundary is driven by the ACTUAL §5 verdict,
// not merely by the field being present.
func TestCapAuthor_Submit_MaterializerRejected_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-badspec")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitBadSpec
	badLens, err := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: "unparseableLens",
		Adapter:       "nats-kv",
		Bucket:        "unparseable",
		Spec:          "this is not openCypher at all",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	sub := submitEnv(t, testutil.GenReqID("CASubmit"), capIDSubmitBadSpec, "lens", badLens,
		"an artifact the materializer refuses")
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (materializer rejected the artifact)", got)
	}
}

// TestCapAuthor_Submit_DuplicateProposalId_Rejected: a second submit reusing a
// live proposal's id cannot overwrite it. The guarantee is the commit batch's
// CreateOnly conditioning on the vertex create (Contract #3 §3.2), not a
// read-before-create probe — this op reads nothing at all — so the whole
// second op rejects and the first proposal's verdict is untouched.
func TestCapAuthor_Submit_DuplicateProposalId_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-dupe")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitDuplicate
	first := submitEnv(t, testutil.GenReqID("CASubDupeA"), capIDSubmitDuplicate, "lens",
		validLensContent(t, "firstOperatorLens"), "the original submission")
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("first submit review.state = %q, want pending", got)
	}

	// A DIFFERENT requestId, so the Contract #4 tracker cannot collapse it —
	// the rejection has to come from CreateOnly.
	second := submitEnv(t, testutil.GenReqID("CASubDupeB"), capIDSubmitDuplicate, "lens",
		validLensContent(t, "secondOperatorLens"), "an id collision")
	testutil.PublishOp(t, conn, second)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The original artifact survives intact — the loser wrote nothing.
	ad := aspectData(t, ctx, conn, proposalKey+".artifact")
	content, _ := ad["content"].(string)
	if content != string(validLensContent(t, "firstOperatorLens")) {
		t.Fatalf(".artifact.content was overwritten by the losing submit: %q", content)
	}
}

// TestCapAuthor_Submit_MalformedPayload_Rejected: unlike
// RecordCapabilityProposal — which must never fail() post-Ack, because the
// bridge has already acknowledged the external event — a direct operator
// submission with a missing required field is rejected synchronously, writing
// nothing at all.
func TestCapAuthor_Submit_MalformedPayload_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-malformed")

	sub := submitEnvRaw(testutil.GenReqID("CASubmit"), map[string]any{
		"proposalId": capIDSubmitMalformed,
		// no kind, no content, no rationale
		"validation": map[string]any{"state": "valid"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket,
		"vtx.capabilityproposal."+capIDSubmitMalformed); err == nil {
		t.Fatalf("a rejected submit wrote a proposal vertex; it must write nothing")
	}
}

// TestCapAuthor_Submit_MissingTargetMode_Rejected: where the artifact lands is
// not optional. RecordCapabilityProposal tolerates an empty target only because
// it can never fail() post-Ack; this op rejects, so the operator is told at
// submit time instead of discovering it at review or apply.
func TestCapAuthor_Submit_MissingTargetMode_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-notarget")

	sub := submitEnvRaw(testutil.GenReqID("CASubNoTgt"), map[string]any{
		"proposalId": capIDSubmitNoTarget,
		"kind":       "lens",
		"content":    string(validLensContent(t, "noTargetLens")),
		"rationale":  "submitted with no target.mode",
		"validation": map[string]any{"state": "valid"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket,
		"vtx.capabilityproposal."+capIDSubmitNoTarget); err == nil {
		t.Fatalf("a rejected submit wrote a proposal vertex; it must write nothing")
	}
}

// TestCapAuthor_Submit_GrantExceedsSubmitterScope_Invalid: the grant-kind
// scope containment applies to the human lane too. This op, like both sibling
// ops, records the verdict the trusted caller computed rather than recomputing
// it (a Starlark script has no registry access), so what this proves is the
// recording half: an over-scope grant travels as validation.state != "valid"
// and lands invalid, never pending, never dispatchable.
//
// The half a submitted verdict CANNOT establish is that the caller ran the
// check honestly — the submitter is also the party the check constrains. That
// containment is the APPROVE-time re-validation (cmd/loupe's
// freshCapabilityVerdict / cmd/lattice/capability's freshApprovalVerdict),
// which re-reads the requester's LIVE held permissions; for a proposal this op
// created, .request.requesterId is op.actor, so it re-checks the real
// submitter.
func TestCapAuthor_Submit_GrantExceedsSubmitterScope_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-grant")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitGrant
	content, err := json.Marshal(pkgmgr.GrantArtifactContent{
		OperationType: "DeleteEverything",
		Scope:         "any",
		GrantsTo:      []string{"operator"},
	})
	if err != nil {
		t.Fatalf("marshal grant content: %v", err)
	}
	held := []pkgmgr.HeldPermission{{OperationType: "DeleteEverything", Scope: "self"}}
	report, err := pkgmgr.ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected the materializer to reject a grant exceeding the submitter's held scope, got valid")
	}
	validation := map[string]any{"state": "invalid"}
	if len(report.Errors) > 0 {
		b, _ := json.Marshal(report.Errors)
		validation["report"] = string(b)
	}

	sub := submitEnvRaw(testutil.GenReqID("CASubGrant"), map[string]any{
		"proposalId": capIDSubmitGrant,
		"kind":       "grant",
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "an over-scope grant the submitter does not hold",
		"validation": validation,
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (grant exceeds the submitter's scope)", got)
	}
}

// TestCapAuthor_Submit_EntersTheSameHumanGate: the studio can put an artifact
// INTO the review queue but never through it — an operator-submitted proposal
// is approved by the shipped, unmodified ReviewCapabilityProposal flow, on the
// same pending-only transition and the same fresh-verdict re-validation.
func TestCapAuthor_Submit_EntersTheSameHumanGate(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-review")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitGated
	sub := submitEnv(t, testutil.GenReqID("CASubGate"), capIDSubmitGated, "lens",
		validLensContent(t, "gatedOperatorLens"), "an operator-composed lens")
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rev := reviewEnv(testutil.GenReqID("CASubGateRev"), capIDSubmitGated, "approve",
		map[string]any{"state": "valid"})
	testutil.PublishOp(t, conn, rev)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "approved" {
		t.Fatalf("review.state = %q, want approved", got)
	}
	// The reviewer link is recorded exactly as it is for an AI-authored
	// proposal — the queue does not special-case origin.
	lnk := readDoc(t, ctx, conn, "lnk.capabilityproposal."+capIDSubmitGated+".reviewedBy.identity."+capStaffActorID)
	if got, _ := lnk["sourceVertex"].(string); got != proposalKey {
		t.Fatalf("reviewedBy link sourceVertex = %q, want %q", got, proposalKey)
	}
}

// TestCapAuthor_Submit_TargetPackageNameTrimmed: proposal_package_name strips
// surrounding whitespace exactly as required_string does, so a
// target.packageName carrying it (a studio form field, a copy-paste) is
// stored trimmed. Installer.findInstalledPackage's manifest scan matches
// byte-exactly against a live manifest's stored name — a stray leading/
// trailing space here would otherwise never resolve, is not tolerated as a
// near-miss recovery either (a near-miss refuses loudly, it does not
// resolve), and would leave every apply against this target permanently
// unable to find the package it names.
func TestCapAuthor_Submit_TargetPackageNameTrimmed(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-pkgname")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitPkgName
	sub := submitEnvRaw(testutil.GenReqID("CASubmit"), map[string]any{
		"proposalId": capIDSubmitPkgName,
		"kind":       "lens",
		"content":    string(validLensContent(t, "pkgNameTrimmedLens")),
		"target": map[string]any{
			"mode":        "upgradeExisting",
			"packageName": "  loftspace-domain  ",
		},
		"rationale":  "target.packageName carries stray whitespace",
		"validation": map[string]any{"state": "valid"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	td := aspectData(t, ctx, conn, proposalKey+".target")
	if got, _ := td["packageName"].(string); got != "loftspace-domain" {
		t.Fatalf(".target.packageName = %q, want the trimmed form %q", got, "loftspace-domain")
	}
}

// TestCapAuthor_Submit_ValidationStateTrailingWhitespace_StaysInvalid pins the
// regression a global proposal_string strip would reopen: validation.state is
// a literal-equality fail-closed gate against "valid" (the §5 verdict this op
// trusts, never re-derives), not an identifier proposal_package_name's own
// trim may touch. A trailing newline — the shape a copy-pasted JSON blob or a
// bridge reply's own encoding could carry — must still read as an
// unrecognized state and record the proposal invalid, never pending.
func TestCapAuthor_Submit_ValidationStateTrailingWhitespace_StaysInvalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-submit-valws")

	proposalKey := "vtx.capabilityproposal." + capIDSubmitValWS
	sub := submitEnvRaw(testutil.GenReqID("CASubmit"), map[string]any{
		"proposalId": capIDSubmitValWS,
		"kind":       "lens",
		"content":    string(validLensContent(t, "valStateWhitespaceLens")),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "validation.state carries a trailing newline",
		"validation": map[string]any{"state": "valid\n"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (\"valid\\n\" must not be folded into \"valid\")", got)
	}
	rv := aspectData(t, ctx, conn, proposalKey+".review")
	if got, _ := rv["invalidReason"].(string); got == "" {
		t.Fatalf(".review.invalidReason is empty; want the no-valid-verdict reason")
	}
}
