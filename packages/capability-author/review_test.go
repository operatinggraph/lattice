// AI-authored-capabilities Fire 2 Increment 1 — the ReviewCapabilityProposal
// human-verdict op (design §3.3), mirroring augur's own ReviewProposal tests
// exactly: an operator flips a PENDING proposal to approved | rejected,
// addressed directly by its own proposalId (no claim indirection). A reject
// is always permitted; an approve re-runs the §5 boundary against the LIVE
// catalog by requiring the TRUSTED caller to attach a FRESH validation
// verdict in the payload (the script has no parser/registry access) — a
// missing or non-"valid" fresh verdict fail-closes the approve to invalid.
// The apply path (F-004 InstallPackage/UpgradePackage + the applied flip)
// remains a later increment.
package capabilityauthor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// Per-scenario proposal ids + claim handles (valid 20-char bare NanoIDs —
// Contract #1's alphabet excludes I/l/O/0 for visual ambiguity).
const (
	capIDRvApprove = "CArvApproveHJKMNPQRS"
	capIDRvReject  = "CArvRejectHJKMNPQRST"
	capIDRvNonPend = "CArvNonPendHJKMNPQRS"
	capIDRvDouble  = "CArvDupeHJKMNPQRSTUV"
	capIDRvStale   = "CArvDriftHJKMNPQRSTU"
	capIDRvMissing = "CArvMissingHJKMNPQRS"
	capIDRvBadVerd = "CArvBadVerdHJKMNPQRS"
	capIDRvUnknown = "CArvUnknownHJKMNPQRS"
	capIDRvBadType = "CArvBadTypeHJKMNPQRS"

	capHandleRvApprove = "CAHNDRvApprHJKMNPQRS"
	capHandleRvReject  = "CAHNDRvRjctHJKMNPQRS"
	capHandleRvNonPend = "CAHNDRvNpndHJKMNPQRS"
	capHandleRvDouble  = "CAHNDRvDupeHJKMNPQRS"
	capHandleRvStale   = "CAHNDRvDriftHJKMNPQR"
	capHandleRvMissing = "CAHNDRvMissHJKMNPQRS"
	capHandleRvBadVerd = "CAHNDRvBvrdHJKMNPQRS"
	capHandleRvBadType = "CAHNDRvBadTypHJKMNPQ"
)

// reviewEnv builds the ReviewCapabilityProposal op. The operator submits
// {proposalId, verdict, validation?}; the reviewer identity is the TRUSTED
// actor on the envelope (op.actor) and the stamp is op.submittedAt — neither
// is a payload field.
func reviewEnv(reqID, proposalID, verdict string, validation map[string]any) *processor.OperationEnvelope {
	payload := map[string]any{"proposalId": proposalID, "verdict": verdict}
	if validation != nil {
		payload["validation"] = validation
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReviewCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// reviewEnvRawValidation builds a ReviewCapabilityProposal op with an
// arbitrary (possibly non-object) validation payload value — for exercising
// the malformed-payload fail-closed path a typed map[string]any can't express.
func reviewEnvRawValidation(reqID, proposalID, verdict string, validation any) *processor.OperationEnvelope {
	payload := map[string]any{"proposalId": proposalID, "verdict": verdict, "validation": validation}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReviewCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// driveReview submits a ReviewCapabilityProposal and drives it to the wanted outcome.
func driveReview(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, verdict string, validation map[string]any, want processor.MessageOutcome) {
	t.Helper()
	rv := reviewEnv(testutil.GenReqID("CARev"+tag), proposalID, verdict, validation)
	testutil.PublishOp(t, conn, rv)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// reviewField reads a string field off vtx.capabilityproposal.<id>.review.data.
func reviewField(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey, field string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".review")
	data, _ := doc["data"].(map[string]any)
	v, _ := data[field].(string)
	return v
}

// drivePendingProposal drives Request → Claim → a valid-lens Record and
// returns the proposal key, asserting the pending precondition.
func drivePendingProposal(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, handle string) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID
	req := requestEnv(testutil.GenReqID("CAReq"+tag), proposalID, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClm"+tag), handle, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARec"+tag), handle, "lens", validLensContent(t, "reviewCandidate"+tag), 0.86)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("precondition: review.state = %q, want pending", got)
	}
	return proposalKey
}

// TestCapAuthor_Review_Approve: an operator approves a pending proposal — the
// verdict flips pending → approved, the reviewer + stamp are recorded on
// .review, and a reviewedBy link to the actor is created (proposal is source).
func TestCapAuthor_Review_Approve(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-approve")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "appr", capIDRvApprove, capHandleRvApprove)
	driveReview(t, ctx, conn, cp, cons, "appr", capIDRvApprove, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved", got)
	}
	if got := reviewField(t, ctx, conn, pk, "reviewedAt"); got == "" {
		t.Fatalf("reviewedAt must be stamped on review")
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got != "" {
		t.Fatalf("invalidReason = %q, want empty on a clean approve", got)
	}
	if got := reviewField(t, ctx, conn, pk, "appliedAt"); got != "" {
		t.Fatalf("appliedAt = %q, want empty (apply is a later increment)", got)
	}
	// reviewedBy link: proposal is the source, the trusted actor is the target.
	lnk := "lnk.capabilityproposal." + capIDRvApprove + ".reviewedBy.identity." + capStaffActorID
	link := readDoc(t, ctx, conn, lnk)
	if got, _ := link["sourceVertex"].(string); got != pk {
		t.Fatalf("reviewedBy sourceVertex = %q, want %q (proposal is source)", got, pk)
	}
	if got, _ := link["targetVertex"].(string); got != capStaffActorKey {
		t.Fatalf("reviewedBy targetVertex = %q, want %q (the reviewing actor)", got, capStaffActorKey)
	}
	if ld, _ := link["data"].(map[string]any); ld["verdict"] != "approve" {
		t.Fatalf("reviewedBy.data.verdict = %v, want approve", ld["verdict"])
	}
}

// TestCapAuthor_Review_Reject: an operator rejects a pending proposal — flips
// to rejected with no re-validation required (a reject is always permitted).
func TestCapAuthor_Review_Reject(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-reject")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "rjct", capIDRvReject, capHandleRvReject)
	driveReview(t, ctx, conn, cp, cons, "rjct", capIDRvReject, "reject", nil, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "rejected" {
		t.Fatalf("review.state = %q, want rejected", got)
	}
	if got := reviewField(t, ctx, conn, pk, "reviewedAt"); got == "" {
		t.Fatalf("reviewedAt must be stamped on reject")
	}
}

// TestCapAuthor_Review_NonPending_Rejected: only a pending proposal is
// reviewable. Reviewing an already-invalid proposal is rejected
// (InvalidReviewTransition) and the stored verdict is unchanged.
func TestCapAuthor_Review_NonPending_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-nonpend")

	proposalKey := "vtx.capabilityproposal." + capIDRvNonPend
	req := requestEnv(testutil.GenReqID("CAReq"), capIDRvNonPend, "grant RescheduleAppointment to front-desk")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClm"), capHandleRvNonPend, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A disabled-kind reply lands review.state=invalid.
	rec := recordEnv(t, testutil.GenReqID("CARec"), capHandleRvNonPend, "grant", json.RawMessage(`{}`), 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("precondition: review.state = %q, want invalid", got)
	}

	driveReview(t, ctx, conn, cp, cons, "npnd", capIDRvNonPend, "approve", map[string]any{"state": "valid"}, processor.OutcomeRejected)
	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (unchanged — an invalid proposal cannot be reviewed)", got)
	}
}

// TestCapAuthor_Review_UnknownProposal_Rejected: reviewing a proposalId with
// no recorded review is rejected — a verdict can never fabricate a proposal.
func TestCapAuthor_Review_UnknownProposal_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-unknown")

	driveReview(t, ctx, conn, cp, cons, "unkn", capIDRvUnknown, "approve", map[string]any{"state": "valid"}, processor.OutcomeRejected)
}

// TestCapAuthor_Review_DoubleReview_Rejected: a proposal is reviewed once. A
// second genuine review (distinct requestId) finds the proposal already
// approved (not pending) and is rejected — the pending-only guard.
func TestCapAuthor_Review_DoubleReview_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-double")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "dbl1", capIDRvDouble, capHandleRvDouble)
	driveReview(t, ctx, conn, cp, cons, "dbl1", capIDRvDouble, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	driveReview(t, ctx, conn, cp, cons, "dbl2", capIDRvDouble, "reject", nil, processor.OutcomeRejected)

	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (the second review must not overwrite)", got)
	}
}

// TestCapAuthor_Review_ApproveStaleValidation_FailCloses: an approve whose
// attached fresh validation verdict is non-"valid" (the live catalog drifted
// between propose and approve) fail-closes to invalid — never approved.
func TestCapAuthor_Review_ApproveStaleValidation_FailCloses(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-stale")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "stal", capIDRvStale, capHandleRvStale)
	driveReview(t, ctx, conn, cp, cons, "stal", capIDRvStale, "approve",
		map[string]any{"state": "invalid", "report": "lens bucket no longer exists"}, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (stale re-validation must fail-close)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got == "" {
		t.Fatalf("invalidReason must explain the re-validation failure")
	}
}

// TestCapAuthor_Review_ApproveMissingValidation_FailCloses: an approve with no
// fresh validation attached at all is never trusted blind — fail-closes to
// invalid exactly like an explicit non-"valid" verdict.
func TestCapAuthor_Review_ApproveMissingValidation_FailCloses(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-missing")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "miss", capIDRvMissing, capHandleRvMissing)
	driveReview(t, ctx, conn, cp, cons, "miss", capIDRvMissing, "approve", nil, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (approve with no fresh verdict must fail-close)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got == "" {
		t.Fatalf("invalidReason must explain the missing verdict")
	}
}

// TestCapAuthor_Review_InvalidVerdict_Rejected: a verdict outside {approve,
// reject} is a caller contract violation, rejected outright.
func TestCapAuthor_Review_InvalidVerdict_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-badverdict")

	drivePendingProposal(t, ctx, conn, cp, cons, "bvrd", capIDRvBadVerd, capHandleRvBadVerd)
	driveReview(t, ctx, conn, cp, cons, "bvrd", capIDRvBadVerd, "maybe", nil, processor.OutcomeRejected)
}

// TestCapAuthor_Review_ApproveNonObjectValidation_FailCloses: a malformed
// (non-object) validation payload — a JSON array or string instead of
// {state,report?} — must fail-close to invalid like any other unverified
// approve, not raise a script error. Guards the type check ahead of
// proposal_string's dict lookup (an untyped p.validation would otherwise
// reach a raw non-dict value).
func TestCapAuthor_Review_ApproveNonObjectValidation_FailCloses(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rv-badtype")

	pk := drivePendingProposal(t, ctx, conn, cp, cons, "btyp", capIDRvBadType, capHandleRvBadType)
	rv := reviewEnvRawValidation(testutil.GenReqID("CARevbtyp"), capIDRvBadType, "approve", []string{"valid"})
	testutil.PublishOp(t, conn, rv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (non-object validation payload must fail-close, not error)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "invalidReason"); got == "" {
		t.Fatalf("invalidReason must explain the missing verdict")
	}
}
