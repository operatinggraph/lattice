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
	"errors"
	"fmt"
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
	capIDApplyProtUpg    = "CAApprvProtUpgHJKMNP"
	capHandleApplyProtUp = "CAHNDApProtUpgHJKMNP"
	capIDApplyProtNew    = "CAApprvProtNewHJKMNP"
	capHandleApplyProtNw = "CAHNDApProtNewHJKMNP"
	capIDApplyModeWS     = "CAApprvModeWsHJKMNPQ"
	capHandleApplyModeWS = "CAHNDApModeWsHJKMNPQ"
	capIDApplyShrink     = "CAApprvShrinkHJKMNPQ"
	capHandleApplyShrink = "CAHNDApShrinkHJKMNPQ"
	capIDApplyRemedy     = "CAApprvRemedyHJKMNPQ"
	capHandleApplyRemedy = "CAHNDApRemedyHJKMNPQ"

	capIDApplyPadUnpadded     = "CAApprvPadAHJKMNPQRS"
	capHandleApplyPadUnpadded = "CAHNDApPadAHJKMNPQRS"
	capIDApplyPadPadded       = "CAApprvPadBHJKMNPQRS"
	capHandleApplyPadPadded   = "CAHNDApPadBHJKMNPQRS"
	capIDApplyPadMismatch     = "CAApprvPadCHJKMNPQRS"
	capHandleApplyPadMismatch = "CAHNDApPadCHJKMNPQRS"
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

// recordEnvForApplyTarget mirrors recordEnv (proposal_test.go) but attaches a
// real target object — the shared recordEnv leaves the target empty (Fire 1
// never needed to apply anything), so this variant is local to the apply tests
// rather than widening every existing recordEnv call site. The whole target is
// a parameter because a case's subject may be a target FIELD (baseVersion,
// newVersion) rather than the mode/packageName pair; the kind is one because an
// EDIT proposal carries a weaverTarget where every other case here carries a
// lens.
func recordEnvForApplyTarget(t *testing.T, reqID, handle, kind string, target map[string]any, content json.RawMessage, confidence float64) *processor.OperationEnvelope {
	t.Helper()
	report, err := pkgmgr.ValidateCapabilityArtifact(kind, content, fullCypherParser{}, nil, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid %s artifact, got errors: %v", kind, report.Errors)
	}
	result := map[string]any{
		"kind":       kind,
		"content":    string(content),
		"target":     target,
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
// for CapabilityApplyPlanForProposal to build an installable Definition — in
// the common newPackage mode.
func drivePendingProposalForApply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, handle, packageName string) string {
	t.Helper()
	return drivePendingProposalForApplyMode(t, ctx, conn, cp, cons, tag, proposalID, handle, packageName, "newPackage")
}

// drivePendingProposalForApplyMode is drivePendingProposalForApply with an
// explicit target.mode, for the cases where the mode itself is under test.
func drivePendingProposalForApplyMode(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, handle, packageName, mode string) string {
	t.Helper()
	return drivePendingProposalForApplyTarget(t, ctx, conn, cp, cons, tag, proposalID, handle, "lens",
		map[string]any{"mode": mode, "packageName": packageName}, validLensContent(t, "applyLens"+tag))
}

// drivePendingProposalForApplyTarget is drivePendingProposalForApplyMode with
// the whole target object and the artifact content supplied — the shape a case
// needs when it asserts over a target FIELD, or when two proposals must carry
// the SAME artifact (a refusal and the remedy it names).
func drivePendingProposalForApplyTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, proposalID, handle, kind string, target map[string]any, content json.RawMessage) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID
	req := requestEnv(testutil.GenReqID("CAReq"+tag), proposalID, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClm"+tag), handle, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnvForApplyTarget(t, testutil.GenReqID("CARec"+tag), handle, kind, target, content, 0.86)
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
// pkgmgr.Installer.ApplyCapabilityPlan — the sanctioned entry point, which
// carries the options a partial, AI-authored Definition requires into the SAME
// unmodified F-004 path every human package install runs. Returns the
// resulting pkgmgr.ApplyResult.
func applyRealPackage(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) *pkgmgr.ApplyResult {
	t.Helper()
	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}

	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()

	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err != nil {
		t.Fatalf("Installer.ApplyCapabilityPlan(%s): %v", plan.PackageName, err)
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

// TestCapAuthor_Apply_PlatformProtectedUpgrade_Rejected: an APPROVED proposal
// targeting capability-author — the authoring machinery's OWN package, live in
// this env — as upgradeExisting never reaches a Definition. The refusal lands
// in the read-only plan builder, before any InstallPackage/UpgradePackage op is
// submitted, so the assertion is on CapabilityApplyPlanForProposal directly
// (applyRealPackage's first step) rather than on an op outcome.
func TestCapAuthor_Apply_PlatformProtectedUpgrade_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-protupg")

	pk := drivePendingProposalForApplyMode(t, ctx, conn, cp, cons, "protupg", capIDApplyProtUpg, capHandleApplyProtUp, "capability-author", "upgradeExisting")
	driveReview(t, ctx, conn, cp, cons, "protupg", capIDApplyProtUpg, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}

	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, pk)
	if err == nil {
		t.Fatalf("CapabilityApplyPlanForProposal returned plan %+v, want a platform-protected refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "capability-author") {
		t.Fatalf("error = %v, want one naming capability-author as platform-protected", err)
	}
}

// TestCapAuthor_Apply_PlatformProtectedNew_Rejected: the same refusal in
// newPackage mode, against objects-base — a protected package this env has NOT
// installed, so the mode/install-catalog check would have let it through. Only
// the name-based guard refuses it, which is what closes the uninstall-then-
// reinstall window.
func TestCapAuthor_Apply_PlatformProtectedNew_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-protnew")

	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "protnew", capIDApplyProtNew, capHandleApplyProtNw, "objects-base")
	driveReview(t, ctx, conn, cp, cons, "protnew", capIDApplyProtNew, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, pk)
	if err == nil {
		t.Fatalf("CapabilityApplyPlanForProposal returned plan %+v, want a platform-protected refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "objects-base") {
		t.Fatalf("error = %v, want one naming objects-base as platform-protected", err)
	}
}

// TestCapAuthor_Apply_ModeWithStrayWhitespace_Rejected pins the regression a
// global proposal_string strip would reopen: target.mode is a literal-
// equality switch in CapabilityApplyPlanForProposal ("newPackage" |
// "upgradeExisting"), read back exactly as SubmitCapabilityProposal stored
// it. A mode padded with stray whitespace must still be unrecognized — never
// folded into a recognized mode the plan builder would act on.
func TestCapAuthor_Apply_ModeWithStrayWhitespace_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-modews")

	pk := drivePendingProposalForApplyMode(t, ctx, conn, cp, cons, "modews", capIDApplyModeWS, capHandleApplyModeWS, "ai-lens-modews", " newPackage ")
	driveReview(t, ctx, conn, cp, cons, "modews", capIDApplyModeWS, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}

	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, pk)
	if err == nil {
		t.Fatalf("CapabilityApplyPlanForProposal returned plan %+v, want an unrecognized-mode refusal", plan)
	}
	if !strings.Contains(err.Error(), "unrecognized target.mode") {
		t.Fatalf("error = %v, want it to name the mode as unrecognized (not folded into \"newPackage\")", err)
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

// patchTargetPackageName rewrites a live proposal's .target aspect's
// packageName field directly in Core KV. Neither RecordCapabilityProposal nor
// SubmitCapabilityProposal can write a packageName that is not already its own
// stripped form — both route it through ddls.go's proposal_package_name before
// the aspect reaches KV — so this is the only way to construct a .target whose
// stored packageName differs from the one the proposal was authored with.
func patchTargetPackageName(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey, packageName string) {
	t.Helper()
	key := proposalKey + ".target"
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s carries no data object", key)
	}
	data["packageName"] = packageName
	doc["data"] = data
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVUpdate(ctx, testutil.HarnessCoreBucket, key, raw, entry.Revision); err != nil {
		t.Fatalf("KVUpdate %s: %v", key, err)
	}
}

// TestCapAuthor_Target_PackageNameStrippedAtWrite pins where the whitespace
// question is actually settled: at the two .target WRITERS, not at any reader.
// Both SubmitCapabilityProposal and RecordCapabilityProposal fold packageName
// through proposal_package_name before the aspect is ever stored, so a padded
// authored name never becomes a stored one.
//
// That is what lets every downstream comparison stay byte-exact, matching
// Installer.findInstalledPackage, whose own contract says matching is
// deliberately NOT folded: a package name resolves a destructive target
// (install/upgrade diff-apply into it, uninstall tombstones its declaredKeys),
// so widening the match set would widen what gets mutated. The complementary
// guarantee is Definition.validatePackageName, which refuses a non-normalized
// Name at install time — so a live package whose recorded name is padded is
// not a reachable state either, from either end.
func TestCapAuthor_Target_PackageNameStrippedAtWrite(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-target-padname")

	// SubmitCapabilityProposal — the human lane, one op, no claim.
	subKey := "vtx.capabilityproposal." + capIDApplyPadUnpadded
	sub := submitEnvRaw(testutil.GenReqID("CASubPadA"), map[string]any{
		"proposalId": capIDApplyPadUnpadded,
		"kind":       "lens",
		"content":    string(validLensContent(t, "padNameSubmitLens")),
		"target":     map[string]any{"mode": "newPackage", "packageName": "  ai-lens-padname-submit\t"},
		"rationale":  "a padded target name must not survive the write",
		"validation": map[string]any{"state": "valid"},
	})
	testutil.PublishOp(t, conn, sub)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := aspectValue(t, ctx, conn, subKey+".target", "packageName"); got != "ai-lens-padname-submit" {
		t.Fatalf("SubmitCapabilityProposal stored .target.packageName = %q, want the stripped %q", got, "ai-lens-padname-submit")
	}

	// RecordCapabilityProposal — the AI lane, through the claim handle.
	recKey := drivePendingProposalForApplyTarget(t, ctx, conn, cp, cons, "padR", capIDApplyPadPadded, capHandleApplyPadPadded,
		"lens", map[string]any{"mode": "newPackage", "packageName": "  ai-lens-padname-record\t"},
		validLensContent(t, "padNameRecordLens"))
	if got := aspectValue(t, ctx, conn, recKey+".target", "packageName"); got != "ai-lens-padname-record" {
		t.Fatalf("RecordCapabilityProposal stored .target.packageName = %q, want the stripped %q", got, "ai-lens-padname-record")
	}
}

// TestCapAuthor_Apply_ForeignTargetPackageName_Rejected keeps the reader-side
// negative the byte-exact comparison exists for: a .target.packageName that is
// a genuinely different name from the installed package's own is refused
// PackageMismatch, and correcting only that name admits the identical
// submission. The patch is the only way to construct the divergence — both
// write paths fold on the way in — so this is the reader guard under test, not
// a writer that let something through.
func TestCapAuthor_Apply_ForeignTargetPackageName_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-foreignname")

	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "padM", capIDApplyPadMismatch, capHandleApplyPadMismatch, "ai-lens-padname-mismatch")
	driveReview(t, ctx, conn, cp, cons, "padM", capIDApplyPadMismatch, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	res := applyRealPackage(t, ctx, conn, pk)
	installRequestID := "install:" + res.PackageName + "@" + res.ToVersion

	patchTargetPackageName(t, ctx, conn, pk, "a-totally-different-package-name")
	env := applyEnv(testutil.GenReqID("CAApplypadM"), capIDApplyPadMismatch, res.PackageKey, installRequestID)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("foreign name: outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "PackageMismatch") {
		t.Fatalf("foreign name: error = %+v, want a PackageMismatch message", reply.Error)
	}
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("foreign name: review.state = %q, want approved (unchanged by the rejected apply)", got)
	}

	// The positive vector: only the name corrected, everything else identical.
	patchTargetPackageName(t, ctx, conn, pk, res.PackageName)
	driveApply(t, ctx, conn, cp, cons, "padMok", capIDApplyPadMismatch, res.PackageKey, installRequestID, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("corrected name: review.state = %q, want applied", got)
	}
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

// capUpgradeTargetName is the vertical, non-platform-protected package the
// coverage e2e upgrades. Its Definition is deliberately multi-entity — two
// lenses and a role — so a proposal describing ONE lens leaves most of the
// package undescribed, which is the whole shape under test.
const capUpgradeTargetName = "capauthor-upgrade-target"

func capUpgradeTargetPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        capUpgradeTargetName,
		Version:     "0.1.0",
		Description: "A multi-entity vertical package an AI-authored proposal may target.",
		Roles: []pkgmgr.RoleSpec{{
			CanonicalName: "capUpgradeReviewer",
			Description:   "Reviews the target package's records.",
		}},
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName: "capUpgradeRosterLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "capauthor-upgrade-roster",
				Engine:        "full",
				Spec:          "MATCH (p:provider) RETURN p.key AS key",
			},
			{
				CanonicalName: "capUpgradeAuditLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "capauthor-upgrade-audit",
				Engine:        "full",
				Spec:          "MATCH (a:audit) RETURN a.key AS key",
			},
		},
	}
}

// declaredKeysOf returns a package's recorded manifest declaredKeys.
func declaredKeysOf(t *testing.T, ctx context.Context, conn *substrate.Conn, packageName string) []string {
	t.Helper()
	pkgKey := "vtx.package." + substrate.PackageEntityNanoID(packageName, "package")
	data, _ := readDoc(t, ctx, conn, pkgKey+".manifest")["data"].(map[string]any)
	raw, _ := data["declaredKeys"].([]any)
	if len(raw) == 0 {
		t.Fatalf("package %q records no declaredKeys; the fixture is not what the test assumes", packageName)
	}
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		s, _ := k.(string)
		keys = append(keys, s)
	}
	return keys
}

// TestCapAuthor_Apply_UpgradeExisting_WithoutCoverage_RefusedAndRemedyApplies
// is the coverage rule on the real stack, and the remedy its refusal names.
//
// An AI-authored proposal carries its own artifact and says nothing about the
// rest of the package it targets, while the in-place apply is a convergence
// operator — so applying one as an upgrade of a package it does not describe
// would retire every key it never mentioned. It is refused, and the package is
// left whole: every declared key is still live afterwards, which is the
// assertion that would catch a refusal that had already committed something.
//
// The second half traces the refusal's stated remedy to its outcome rather
// than to the existence of its first step: the SAME artifact, proposed as a
// package of its own, actually installs and is live. A remedy that only reads
// well is not a remedy.
func TestCapAuthor_Apply_UpgradeExisting_WithoutCoverage_RefusedAndRemedyApplies(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	installPkg(t, ctx, conn, capUpgradeTargetPackage())
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-shrink")

	declared := declaredKeysOf(t, ctx, conn, capUpgradeTargetName)
	artifact := validLensContent(t, "capUpgradeRemedyLens")

	pk := drivePendingProposalForApplyTarget(t, ctx, conn, cp, cons, "shrink", capIDApplyShrink, capHandleApplyShrink, "lens",
		map[string]any{
			"mode":        "upgradeExisting",
			"packageName": capUpgradeTargetName,
			"baseVersion": "0.1.0",
			"newVersion":  "0.2.0",
		}, artifact)
	driveReview(t, ctx, conn, cp, cons, "shrink", capIDApplyShrink, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, pk)
	if err != nil {
		t.Fatalf("a well-formed upgradeExisting proposal must build a plan; the refusal belongs at the apply: %v", err)
	}
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err == nil {
		t.Fatalf("an upgrade that does not describe its target package must be refused, got: %+v", res)
	}
	if !errors.Is(err, pkgmgr.ErrApplyWouldRemove) {
		t.Fatalf("want pkgmgr.ErrApplyWouldRemove, got %v", err)
	}

	// The package is whole: nothing the refusal named was actually retired.
	for _, k := range declared {
		if del, _ := readDoc(t, ctx, conn, k)["isDeleted"].(bool); del {
			t.Fatalf("%s was tombstoned by a refused apply — the whole point of refusing is that it commits nothing", k)
		}
	}
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (a refused apply closes nothing)", got)
	}

	// The remedy the refusal prints: the same artifact, as its own package.
	remedyPk := drivePendingProposalForApplyTarget(t, ctx, conn, cp, cons, "remedy", capIDApplyRemedy, capHandleApplyRemedy, "lens",
		map[string]any{"mode": "newPackage", "packageName": "capauthor-remedy-pkg"}, artifact)
	driveReview(t, ctx, conn, cp, cons, "remedy", capIDApplyRemedy, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	remedyPlan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, remedyPk)
	if err != nil {
		t.Fatalf("the remedy proposal must build a plan: %v", err)
	}
	remedyRes, err := inst.ApplyCapabilityPlan(ctx, remedyPlan)
	if err != nil {
		t.Fatalf("the refusal's stated remedy must actually apply: %v", err)
	}
	if remedyRes.Action != "install" || remedyRes.Created == 0 {
		t.Fatalf("the remedy must install the artifact, got %+v", remedyRes)
	}
	lensKey := "vtx.meta." + pkgmgr.LensID("capauthor-remedy-pkg", "capUpgradeRemedyLens")
	if del, _ := readDoc(t, ctx, conn, lensKey)["isDeleted"].(bool); del {
		t.Fatalf("%s must be live — the remedy is only a remedy if the artifact lands", lensKey)
	}
}

// The EDIT round trip (natural-language-target-edit-design.md §6.3). Every
// other case in this file applies a proposal that MINTS a package; this one is
// the first that changes one already installed, and `upgradeExisting` had never
// been produced by anything before the bridge's edit path — the mode was
// validated end-to-end but never exercised end-to-end.

const (
	capEditLensPkgName = "capauthor-edit-lens"
	capEditLensName    = "capEditColdOnboarding"
	capEditPkgName     = "weaver-target-cold-nudge-7f2a"
	capEditTargetID    = "capEditColdNudge"
	capIDApplyEdit     = "CAApprvEditHJKMNPQRS"
	capHandleApplyEdit = "CAHNDApEditHJKMNPQRS"
)

// capEditLensPackage is the lens the edited target binds to. It lives in its
// own package because the target's package must hold NOTHING but that target —
// a Definition materialized from a capability proposal describes exactly one
// weaver target, and anything else in the same package would be a key the
// upgrade does not describe.
func capEditLensPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:    capEditLensPkgName,
		Version: "0.1.0",
		Lenses: []pkgmgr.LensSpec{{
			CanonicalName: capEditLensName,
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "capauthor-edit-cold",
			Engine:        "full",
			Spec:          "MATCH (i:identity) RETURN i.key AS key, true AS missing_reminder",
		}},
	}
}

// capEditTargetPackage is the console-minted shape an edit is admissible
// against: one weaver target, no description, no depends — exactly what
// CapabilityApplyPlanForProposal's Definition can re-describe whole.
func capEditTargetPackage(lensRef string) pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:    capEditPkgName,
		Version: "0.1.0",
		WeaverTargets: []pkgmgr.WeaverTargetSpec{{
			TargetID:    capEditTargetID,
			LensRef:     lensRef,
			Description: "Cold onboardings raise a health issue after 24 hours.",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_reminder": {Action: "surface", IssueCode: "coldOnboarding", IssueSeverity: "warning"},
			},
		}},
	}
}

// TestCapAuthor_Apply_EditExistingTarget_UpgradesInPlace is the proof that the
// edit mode works at all: a real single-artifact weaver-target package,
// installed, then upgraded in place by an approved EDIT proposal running the
// unmodified F-004 apply.
//
// The four assertions are the four ways an in-place apply can be wrong while
// still returning success: it tombstones something the proposal did not
// describe (old \ new ≠ ∅), it narrows the declaration, it does not actually
// change the artifact, or it leaves the package on its old version. A test
// asserting only that Apply returned nil would pass for all four.
func TestCapAuthor_Apply_EditExistingTarget_UpgradesInPlace(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	installPkg(t, ctx, conn, capEditLensPackage())
	lensRef := pkgmgr.LensID(capEditLensPkgName, capEditLensName)
	installPkg(t, ctx, conn, capEditTargetPackage(lensRef))
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-apply-edit")

	declaredBefore := declaredKeysOf(t, ctx, conn, capEditPkgName)
	targetKey := "vtx.meta." + substrate.PackageEntityNanoID(capEditPkgName, "weaverTarget:"+capEditTargetID)
	specBefore := aspectData(t, ctx, conn, targetKey+".spec")

	// The edit: same targetId, same lensRef, a changed remediation and a
	// rewritten description — the shape the bridge's edit path files.
	edited, err := json.Marshal(pkgmgr.WeaverTargetArtifactContent{
		TargetID: capEditTargetID,
		LensRef:  lensRef,
		Gaps: map[string]pkgmgr.GapActionArtifact{
			"missing_reminder": {Action: "surface", IssueCode: "coldOnboardingUrgent", IssueSeverity: "error"},
		},
		Description: "Cold onboardings raise a health issue after 48 hours.",
	})
	if err != nil {
		t.Fatalf("marshal edited target: %v", err)
	}

	pk := drivePendingProposalForApplyTarget(t, ctx, conn, cp, cons, "edit", capIDApplyEdit, capHandleApplyEdit, "weaverTarget",
		map[string]any{
			"mode":        "upgradeExisting",
			"packageName": capEditPkgName,
			"baseVersion": "0.1.0",
			"newVersion":  "0.1.1",
		}, edited)
	driveReview(t, ctx, conn, cp, cons, "edit", capIDApplyEdit, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("precondition: review.state = %q, want approved", got)
	}

	res := applyRealPackage(t, ctx, conn, pk)
	if res.Action != "upgrade" {
		t.Fatalf("ApplyResult.Action = %q, want upgrade — an edit changes the package it names, it does not mint one", res.Action)
	}
	if res.Tombstoned != 0 {
		t.Fatalf("ApplyResult.Tombstoned = %d, want 0 — an edit describes the whole package, so old \\ new is empty", res.Tombstoned)
	}
	if res.FromVersion != "0.1.0" || res.ToVersion != "0.1.1" {
		t.Fatalf("version moved %q → %q, want 0.1.0 → 0.1.1", res.FromVersion, res.ToVersion)
	}

	// Nothing the package declared before the edit is tombstoned after it. This
	// is the assertion the whole editable/not-editable boundary exists to keep
	// true: an upgrade that did not describe one of these keys would have
	// retired it.
	for _, k := range declaredBefore {
		if del, _ := readDoc(t, ctx, conn, k)["isDeleted"].(bool); del {
			t.Errorf("%s was tombstoned by an in-place edit — old \\ new must be empty", k)
		}
	}

	// The declaration is unchanged: an edit that quietly narrowed declaredKeys
	// would leave the dropped keys live but unowned, which no later apply could
	// clean up.
	declaredAfter := declaredKeysOf(t, ctx, conn, capEditPkgName)
	if !sameKeySet(declaredBefore, declaredAfter) {
		t.Errorf("declaredKeys changed across the edit:\n before %v\n after  %v", declaredBefore, declaredAfter)
	}

	// The artifact actually changed — an apply that committed nothing would
	// satisfy every assertion above.
	specAfter := aspectData(t, ctx, conn, targetKey+".spec")
	if fmt.Sprint(specBefore) == fmt.Sprint(specAfter) {
		t.Fatalf("the target's .spec body is unchanged after the edit: %v", specAfter)
	}
	gaps, _ := specAfter["gaps"].(map[string]any)
	gap, _ := gaps["missing_reminder"].(map[string]any)
	if got, _ := gap["issueCode"].(string); got != "coldOnboardingUrgent" {
		t.Errorf("installed gap issueCode = %q, want the edited coldOnboardingUrgent", got)
	}
	if got, _ := specAfter["lensRef"].(string); got != lensRef {
		t.Errorf("installed lensRef = %q, want the unchanged %q", got, lensRef)
	}
	if got := aspectValue(t, ctx, conn, targetKey+".description", "text"); got != "Cold onboardings raise a health issue after 48 hours." {
		t.Errorf("installed description = %q, want the edited prose", got)
	}

	// The proposal's own lifecycle closes exactly as a fresh install's does.
	installRequestID := "upgrade:" + res.PackageName + "@" + res.ToVersion
	driveApply(t, ctx, conn, cp, cons, "edit", capIDApplyEdit, res.PackageKey, installRequestID, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("review.state = %q, want applied", got)
	}
}

// aspectValue returns one field of an aspect document's `data` object.
func aspectValue(t *testing.T, ctx context.Context, conn *substrate.Conn, key, field string) string {
	t.Helper()
	v, _ := aspectData(t, ctx, conn, key)[field].(string)
	return v
}

// sameKeySet compares two declared-key lists as sets — install order is not
// part of the declaration.
func sameKeySet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, k := range a {
		seen[k]++
	}
	for _, k := range b {
		seen[k]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
