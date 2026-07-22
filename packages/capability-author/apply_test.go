// AI-authored-capabilities Fire 2 — the apply loop that closes the fire
// (design §3.5): after ReviewCapabilityProposal approves a proposal, the
// operator separately applies its materialized artifact through the
// existing, UNMODIFIED F-004 InstallPackage/UpgradePackage op
// (pkgmgr.CapabilityApplyPlanForProposal + pkgmgr.Installer.Apply — a
// SEPARATE Processor commit, on the meta lane, exactly like any human
// package install), then submits MarkCapabilityProposalApplied (default
// lane) to record the applied-flip. Proves: an approved lens proposal
// becomes a live, queryable package; review.state flips approved→applied
// with appliedAt/appliedByOp + the appliedAs link; only an approved
// proposal may be marked applied (fail-closed, no double-apply).
package capabilityauthor_test

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
)

const (
	capIDApply           = "CAApprvLoopHJKMNPQRS"
	capHandleApply       = "CAHNDApprvHJKMNPQRST"
	capIDApplyTwice      = "CAApprvTwiceHJKMNPQR"
	capHandleApplyTwo    = "CAHNDApprvTwoHJKMNPQ"
	capIDApplyPending    = "CAApprvPndHJKMNPQRST"
	capHandleApplyPend   = "CAHNDApprvPndHJKMNPQ"
	capIDApplyUnknownPkg = "CAApprvUnkPkgHJKMNPQ"
	capHandleApplyUnkPkg = "CAHNDApUnkPkgHJKMNPQ"
	capIDApplyMismatchA  = "CAApprvMismAHJKMNPQR"
	capHandleApplyMismA  = "CAHNDApMismAHJKMNPQR"
	capIDApplyMismatchB  = "CAApprvMismBHJKMNPQR"
	capHandleApplyMismB  = "CAHNDApMismBHJKMNPQR"
	capFakePackageKey    = "vtx.package.fakePkgHJKMNPQRSTUVW"
	capIDApplyGrant      = "CAApprvGrantHJKMNPQR"
	capHandleApplyGrant  = "CAHNDApGrantHJKMNPQR"
)

// applyEnv builds the MarkCapabilityProposalApplied op the operator submits
// after separately running the real F-004 apply.
func applyEnv(reqID, proposalID, packageKey, installRequestID string) *processor.OperationEnvelope {
	payload := map[string]any{
		"proposalId":       proposalID,
		"packageKey":       packageKey,
		"installRequestId": installRequestID,
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MarkCapabilityProposalApplied",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// recordEnvForApply mirrors recordEnv (proposal_test.go) but attaches a real
// target.packageName — the shared recordEnv leaves it empty (Fire 1 never
// needed to apply anything), so this variant is local to the apply tests
// rather than widening every existing recordEnv call site.
func recordEnvForApply(t *testing.T, reqID, handle, packageName string, content json.RawMessage, confidence float64) *processor.OperationEnvelope {
	t.Helper()
	report, err := pkgmgr.ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid lens artifact, got errors: %v", report.Errors)
	}
	result := map[string]any{
		"kind":       "lens",
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage", "packageName": packageName},
		"rationale":  "reasoned capability authoring proposal",
		"confidence": confidence,
		"validation": map[string]any{"state": "valid"},
	}
	resultBytes, _ := json.Marshal(result)
	payload := map[string]any{
		"externalRef": handle,
		"status":      "completed",
		"result":      string(resultBytes),
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// recordEnvForGrant mirrors recordEnvForApply but for the "grant" kind: it
// attaches the requester's held permissions (simulating the trusted caller's
// fresh Contract #6 capability-projection read) so ValidateCapabilityArtifact
// runs the scope check exactly as production will.
func recordEnvForGrant(t *testing.T, reqID, handle, packageName string, content json.RawMessage, held []pkgmgr.HeldPermission, confidence float64) *processor.OperationEnvelope {
	t.Helper()
	report, err := pkgmgr.ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid grant artifact, got errors: %v", report.Errors)
	}
	result := map[string]any{
		"kind":       "grant",
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage", "packageName": packageName},
		"rationale":  "reasoned capability authoring proposal",
		"confidence": confidence,
		"validation": map[string]any{"state": "valid"},
	}
	resultBytes, _ := json.Marshal(result)
	payload := map[string]any{
		"externalRef": handle,
		"status":      "completed",
		"result":      string(resultBytes),
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// readInstalledGrantPermission resolves the installed package's manifest
// declaredKeys for its one permission vertex (vtx.permission.<id> — 3 dot
// segments, no aspect suffix, unlike a lens's vtx.meta.<id>.canonicalName) and
// returns its key + scope, proof the grant actually landed live.
func readInstalledGrantPermission(t *testing.T, ctx context.Context, conn *substrate.Conn, packageKey string) (permKey, scope string) {
	t.Helper()
	manifest := readDoc(t, ctx, conn, packageKey+".manifest")
	data, _ := manifest["data"].(map[string]any)
	declared, _ := data["declaredKeys"].([]any)
	const prefix = "vtx.permission."
	for _, raw := range declared {
		key, _ := raw.(string)
		if len(key) > len(prefix) && key[:len(prefix)] == prefix && !strings.Contains(key[len(prefix):], ".") {
			doc := readDoc(t, ctx, conn, key)
			d, _ := doc["data"].(map[string]any)
			s, _ := d["scope"].(string)
			return key, s
		}
	}
	t.Fatalf("no permission vertex found among declaredKeys for %s", packageKey)
	return "", ""
}

// TestCapAuthor_Apply_GrantKind_ClosesLoop: the full loop for the "grant"
// kind (design §8 Fire 2 fast-follow) — request → claim → record a valid
// grant (the requester holds the operationType at scope "any", covering the
// artifact's requested "self") → approve → apply the real package → mark
// applied. Proves an AI-authored grant becomes a live, queryable permission
// with a genuine grantedBy link to the named role — not merely that the ops
// replied success.
func TestCapAuthor_Apply_GrantKind_ClosesLoop(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-grant")

	proposalKey := "vtx.capabilityproposal." + capIDApplyGrant
	req := requestEnv(testutil.GenReqID("CAReqGrant"), capIDApplyGrant, "grant AIGrantedRescheduleDemo to operator")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClmGrant"), capHandleApplyGrant, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	content, err := json.Marshal(pkgmgr.GrantArtifactContent{
		OperationType: "AIGrantedRescheduleDemo",
		Scope:         "self",
		GrantsTo:      []string{"operator"},
	})
	if err != nil {
		t.Fatalf("marshal grant content: %v", err)
	}
	held := []pkgmgr.HeldPermission{{OperationType: "AIGrantedRescheduleDemo", Scope: "any"}}
	rec := recordEnvForGrant(t, testutil.GenReqID("CARecGrant"), capHandleApplyGrant, "ai-grant-loop", content, held, 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("precondition: review.state = %q, want pending", got)
	}

	driveReview(t, ctx, conn, cp, cons, "grantapply", capIDApplyGrant, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, proposalKey); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}

	applyResult := applyRealPackage(t, ctx, conn, proposalKey)
	if applyResult.Action != "install" {
		t.Fatalf("ApplyResult.Action = %q, want install (fresh target)", applyResult.Action)
	}

	installRequestID := "install:" + applyResult.PackageName + "@" + applyResult.ToVersion
	driveApply(t, ctx, conn, cp, cons, "grant", capIDApplyGrant, applyResult.PackageKey, installRequestID, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "applied" {
		t.Fatalf("review.state = %q, want applied", got)
	}

	permKey, scope := readInstalledGrantPermission(t, ctx, conn, applyResult.PackageKey)
	if scope != "self" {
		t.Fatalf("installed permission scope = %q, want self", scope)
	}
	lnk := "lnk." + permKey[len("vtx."):] + ".grantedBy.role." + bootstrap.RoleOperatorID
	link := readDoc(t, ctx, conn, lnk)
	if got, _ := link["sourceVertex"].(string); got != permKey {
		t.Fatalf("grantedBy sourceVertex = %q, want %q (permission is source)", got, permKey)
	}
	if got, _ := link["targetVertex"].(string); got != bootstrap.RoleOperatorKey {
		t.Fatalf("grantedBy targetVertex = %q, want %q", got, bootstrap.RoleOperatorKey)
	}
}

// drivePendingProposalForApply mirrors drivePendingProposal (review_test.go)
// but records a lens artifact carrying a real target.packageName — required
// for CapabilityApplyPlanForProposal to build an installable Definition.
func drivePendingProposalForApply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, handle, packageName string) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID
	req := requestEnv(testutil.GenReqID("CAReq"+tag), proposalID, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClm"+tag), handle, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	content := validLensContent(t, "applyLens"+tag)
	rec := recordEnvForApply(t, testutil.GenReqID("CARec"+tag), handle, packageName, content, 0.86)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("precondition: review.state = %q, want pending", got)
	}
	return proposalKey
}

// applyRealPackage runs the actual F-004 apply for an approved proposal: it
// reads the plan (read-only, no submission), stands up a temporary meta-lane
// pipeline exactly like the package installs earlier in the test used, and
// submits the real InstallPackage/UpgradePackage op through
// pkgmgr.Installer.Apply — the SAME unmodified path every human package
// install runs. Returns the resulting pkgmgr.ApplyResult.
func applyRealPackage(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) *pkgmgr.ApplyResult {
	t.Helper()
	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}

	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()

	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
	res, err := inst.Apply(ctx, plan.Definition, pkgmgr.ApplyOptions{})
	if err != nil {
		t.Fatalf("Installer.Apply(%s): %v", plan.PackageName, err)
	}
	return res
}

// TestCapAuthor_Apply_ClosesLoop: the full loop — request → claim → record a
// valid lens → approve → apply the real package → mark applied. Proves the
// lens package is actually live (queryable) and the proposal's review.state
// reads applied with a populated audit trail + appliedAs link.
func TestCapAuthor_Apply_ClosesLoop(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-loop")

	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "loop", capIDApply, capHandleApply, "ai-lens-loop")
	driveReview(t, ctx, conn, cp, cons, "apply", capIDApply, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}

	applyResult := applyRealPackage(t, ctx, conn, pk)
	if applyResult.Action != "install" {
		t.Fatalf("ApplyResult.Action = %q, want install (fresh target)", applyResult.Action)
	}
	if applyResult.PackageKey == "" {
		t.Fatalf("ApplyResult.PackageKey is empty")
	}

	installRequestID := "install:" + applyResult.PackageName + "@" + applyResult.ToVersion
	driveApply(t, ctx, conn, cp, cons, "loop", capIDApply, applyResult.PackageKey, installRequestID, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("review.state = %q, want applied", got)
	}
	if got := reviewField(t, ctx, conn, pk, "appliedAt"); got == "" {
		t.Fatalf("appliedAt must be stamped on apply")
	}
	if got := reviewField(t, ctx, conn, pk, "appliedByOp"); got != installRequestID {
		t.Fatalf("appliedByOp = %q, want %q", got, installRequestID)
	}
	// reviewedAt (set at approval) must survive the apply-flip's aspect rewrite.
	if got := reviewField(t, ctx, conn, pk, "reviewedAt"); got == "" {
		t.Fatalf("reviewedAt must be preserved through the apply-flip")
	}

	lnk := "lnk.capabilityproposal." + capIDApply + ".appliedAs.package." + applyResult.PackageKey[len("vtx.package."):]
	link := readDoc(t, ctx, conn, lnk)
	if got, _ := link["sourceVertex"].(string); got != pk {
		t.Fatalf("appliedAs sourceVertex = %q, want %q (proposal is source)", got, pk)
	}
	if got, _ := link["targetVertex"].(string); got != applyResult.PackageKey {
		t.Fatalf("appliedAs targetVertex = %q, want %q", got, applyResult.PackageKey)
	}

	// The lens is genuinely live: its meta-vertex canonicalName round-trips.
	installed := readInstalledLensCanonicalName(t, ctx, conn, applyResult.PackageKey)
	if installed != "applyLensloop" {
		t.Fatalf("installed lens canonicalName = %q, want %q", installed, "applyLensloop")
	}
}

// TestCapAuthor_Apply_NonApproved_Rejected: MarkCapabilityProposalApplied
// against a still-pending proposal is rejected — only an approved proposal
// may be marked applied.
func TestCapAuthor_Apply_NonApproved_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-pending")

	drivePendingProposal(t, ctx, conn, cp, cons, "applypend", capIDApplyPending, capHandleApplyPend)
	driveApply(t, ctx, conn, cp, cons, "pend", capIDApplyPending, capFakePackageKey, "install:fake@0.1.0", processor.OutcomeRejected)
}

// TestCapAuthor_Apply_UnknownPackage_Rejected: an APPROVED proposal citing a
// syntactically well-formed but never-installed packageKey is rejected —
// packageKey is never trusted blind; it must name a live installed package
// (its .manifest aspect).
func TestCapAuthor_Apply_UnknownPackage_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-unknownpkg")

	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "unkpkg", capIDApplyUnknownPkg, capHandleApplyUnkPkg, "ai-lens-unknownpkg")
	driveReview(t, ctx, conn, cp, cons, "unkpkg", capIDApplyUnknownPkg, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	driveApply(t, ctx, conn, cp, cons, "unkpkg", capIDApplyUnknownPkg, capFakePackageKey, "install:fake@0.1.0", processor.OutcomeRejected)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (unchanged by the rejected apply against an unknown package)", got)
	}
}

// TestCapAuthor_Apply_PackageNameMismatch_Rejected: an APPROVED proposal
// citing a REAL, live installed package that belongs to a DIFFERENT
// proposal's target is rejected — packageKey must correlate to the same
// proposal's own target.packageName, not merely exist.
func TestCapAuthor_Apply_PackageNameMismatch_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-mismatch")

	pkA := drivePendingProposalForApply(t, ctx, conn, cp, cons, "mmA", capIDApplyMismatchA, capHandleApplyMismA, "ai-lens-mismatch-a")
	driveReview(t, ctx, conn, cp, cons, "mmA", capIDApplyMismatchA, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	pkB := drivePendingProposalForApply(t, ctx, conn, cp, cons, "mmB", capIDApplyMismatchB, capHandleApplyMismB, "ai-lens-mismatch-b")
	driveReview(t, ctx, conn, cp, cons, "mmB", capIDApplyMismatchB, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	applyResultB := applyRealPackage(t, ctx, conn, pkB)

	// Proposal A cites proposal B's real, live package — a different name.
	driveApply(t, ctx, conn, cp, cons, "mmA", capIDApplyMismatchA, applyResultB.PackageKey, "install:cross@0.1.0", processor.OutcomeRejected)
	if got := reviewState(t, ctx, conn, pkA); got != "approved" {
		t.Fatalf("review.state = %q, want approved (unchanged by the rejected cross-proposal apply)", got)
	}
}

// TestCapAuthor_Apply_DoubleApply_Rejected: a second MarkCapabilityProposalApplied
// against an already-applied proposal is rejected — no double-apply.
func TestCapAuthor_Apply_DoubleApply_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-double")

	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "double", capIDApplyTwice, capHandleApplyTwo, "ai-lens-double")
	driveReview(t, ctx, conn, cp, cons, "appdbl", capIDApplyTwice, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	applyResult := applyRealPackage(t, ctx, conn, pk)
	installRequestID := "install:" + applyResult.PackageName + "@" + applyResult.ToVersion
	driveApply(t, ctx, conn, cp, cons, "double1", capIDApplyTwice, applyResult.PackageKey, installRequestID, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("precondition: review.state = %q, want applied", got)
	}

	// A different requestId (not a redelivery) — a genuine second
	// MarkCapabilityProposalApplied finds the proposal already applied (not
	// approved) and is rejected (InvalidApplyTransition), not a Contract #4
	// tracker collapse.
	driveApply(t, ctx, conn, cp, cons, "double2", capIDApplyTwice, applyResult.PackageKey, installRequestID, processor.OutcomeRejected)
	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("review.state = %q, want applied (unchanged by the rejected re-apply)", got)
	}
}

// driveApply submits MarkCapabilityProposalApplied and drives it to the
// wanted outcome. tag distinguishes the requestId (Contract #4 dedup is
// per-requestId, not per-proposal) so a genuine SECOND apply attempt against
// the same proposal is a fresh op, not a redelivery collapse.
func driveApply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, packageKey, installRequestID string, want processor.MessageOutcome) {
	t.Helper()
	env := applyEnv(testutil.GenReqID("CAApply"+tag), proposalID, packageKey, installRequestID)
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// readInstalledLensCanonicalName resolves the installed package's manifest
// declaredKeys for the one lens meta-vertex the apply installed, and returns
// its canonicalName aspect value — proof the lens actually landed live, not
// just that the op replied success.
func readInstalledLensCanonicalName(t *testing.T, ctx context.Context, conn *substrate.Conn, packageKey string) string {
	t.Helper()
	manifest := readDoc(t, ctx, conn, packageKey+".manifest")
	data, _ := manifest["data"].(map[string]any)
	declared, _ := data["declaredKeys"].([]any)
	const suffix = ".canonicalName"
	for _, raw := range declared {
		key, _ := raw.(string)
		if len(key) > len(suffix) && key[len(key)-len(suffix):] == suffix {
			doc := readDoc(t, ctx, conn, key)
			d, _ := doc["data"].(map[string]any)
			if v, _ := d["value"].(string); v != "" {
				return v
			}
		}
	}
	t.Fatalf("no canonicalName aspect found among declaredKeys for %s", packageKey)
	return ""
}
