package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// writeBootstrapFile renders the file loadBootstrap reads, from the primordial
// ids the harness actually seeded — so the actor this CLI stamps on its ops is
// the one the kernel knows.
func writeBootstrapFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	raw, err := json.Marshal(map[string]any{
		"primordialIDs": map[string]string{
			"bootstrapIdentity": strings.TrimPrefix(bootstrap.BootstrapIdentityKey, "vtx.identity."),
			"operatorRole":      bootstrap.RoleOperatorID,
		},
	})
	if err != nil {
		t.Fatalf("marshal bootstrap file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write bootstrap file: %v", err)
	}
	return path
}

// putProposalAspect writes one {isDeleted,data} aspect envelope into Core KV.
func putProposalAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"isDeleted": false, "data": data})
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

// multiEntityPackage is a vertical, non-platform-protected package with more in
// it than any single capability artifact could describe.
func multiEntityPackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "latticepkg-apply-target",
		Version:     "0.1.0",
		Description: "A multi-entity vertical package an AI-authored proposal may target.",
		Roles: []pkgmgr.RoleSpec{{
			CanonicalName: "latticePkgReviewer",
			Description:   "Reviews the target package's records.",
		}},
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName: "latticePkgRosterLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "latticepkg-roster",
				Engine:        "full",
				Spec:          "MATCH (p:provider) RETURN p.key AS key",
			},
			{
				CanonicalName: "latticePkgAuditLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "latticepkg-audit",
				Engine:        "full",
				Spec:          "MATCH (a:audit) RETURN a.key AS key",
			},
		},
	}
}

// TestRunApplyProposal_RemovalRefusalStopsBeforeMarkApplied drives the CLI verb
// end to end against a real embedded stack: an approved proposal whose one lens
// does not describe the package it upgrades.
//
// The refusal reaching the caller is half of it. The other half is that
// MarkCapabilityProposalApplied is never submitted — an apply that committed
// nothing followed by a proposal stamped `applied` is a falsified audit record
// on top of a package that never changed, which is the worse of the two harms.
// The assertion is on the operations stream rather than on the proposal's own
// review state, because a submitted-then-rejected op would leave that state
// looking identical to a submit that never happened.
func TestRunApplyProposal_RemovalRefusalStopsBeforeMarkApplied(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
	target := multiEntityPackage()
	if _, err := inst.Install(ctx, target); err != nil {
		t.Fatalf("install the target package: %v", err)
	}
	stop()

	// An approved proposal carrying one lens, targeting that package as an
	// upgrade it has no coverage of.
	const proposalID = "CALatticePkgHJKMNPQR"
	proposalKey := "vtx.capabilityproposal." + proposalID
	content, err := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: "latticePkgProposedLens",
		Adapter:       "nats-kv",
		Bucket:        "latticepkg-proposed",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	putProposalAspect(t, ctx, conn, proposalKey+".review", map[string]any{"state": "approved"})
	putProposalAspect(t, ctx, conn, proposalKey+".artifact", map[string]any{"kind": "lens", "content": string(content)})
	putProposalAspect(t, ctx, conn, proposalKey+".target", map[string]any{
		"packageName": target.Name,
		"mode":        "upgradeExisting",
		"baseVersion": "0.1.0",
		"newVersion":  "0.2.0",
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = runApplyProposal(proposalID, conn.NATS().ConnectedUrl(), writeBootstrapFile(t), logger)
	if err == nil {
		t.Fatal("apply-proposal must return the removal refusal, got nil")
	}
	if !errors.Is(err, pkgmgr.ErrApplyWouldRemove) {
		t.Fatalf("want pkgmgr.ErrApplyWouldRemove, got %v", err)
	}

	// MarkCapabilityProposalApplied rides the default lane; every package
	// install this harness ran rides the meta lane. So an empty ops.default is
	// exactly "the mark-applied submit never happened".
	stream, err := conn.JetStream().Stream(ctx, testutil.HarnessOpsStream)
	if err != nil {
		t.Fatalf("read the operations stream: %v", err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, "ops.default")
	if err == nil {
		t.Fatalf("a default-lane op was submitted after the refusal: %s", string(msg.Data))
	}
	if !errors.Is(err, jetstream.ErrMsgNotFound) {
		t.Fatalf("probe ops.default: %v", err)
	}

	// The positive vector for that probe. "Nothing on ops.default" only means
	// "nothing was submitted" if a submit would in fact have landed there and
	// been visible — otherwise the assertion above passes for a stream that
	// retains nothing, which is the shape of an absence check that pins
	// nothing at all.
	if _, err := conn.JetStream().Publish(ctx, "ops.default", []byte(`{"probe":true}`)); err != nil {
		t.Fatalf("publish the probe's own control message: %v", err)
	}
	if _, err := stream.GetLastMsgForSubject(ctx, "ops.default"); err != nil {
		t.Fatalf("the ops.default probe cannot see a message that IS there, so its emptiness proved nothing: %v", err)
	}
}

// TestReplyErrorCodeAndMessage_NilSafe pins the nil-safe read runApplyProposal
// relies on to report a rejection: a ReplyStatusRejected reply is not obliged
// to carry an Error body, and this is the one line whose whole job is to
// report a failure, so dereferencing reply.Error directly would panic exactly
// where an operator needs a diagnosis instead of a stack trace.
func TestReplyErrorCodeAndMessage_NilSafe(t *testing.T) {
	cases := []struct {
		name  string
		reply *processor.OperationReply
	}{
		{"nil reply", nil},
		{"nil Error", &processor.OperationReply{Status: processor.ReplyStatusRejected}},
		{"empty Error fields", &processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &processor.ReplyError{},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyErrorCode(tc.reply); got != "" {
				t.Errorf("replyErrorCode(%s) = %q, want \"\"", tc.name, got)
			}
			if got := replyErrorMessage(tc.reply); got != "" {
				t.Errorf("replyErrorMessage(%s) = %q, want \"\"", tc.name, got)
			}
		})
	}

	// The positive vector: a reply that DOES carry a real Error body must have
	// its fields read back exactly, proving the accessors read through to the
	// body rather than always returning "".
	real := &processor.OperationReply{
		Status: processor.ReplyStatusRejected,
		Error:  &processor.ReplyError{Code: "PackageMismatch", Message: "installed name does not match target.packageName"},
	}
	if got := replyErrorCode(real); got != "PackageMismatch" {
		t.Errorf("replyErrorCode(real) = %q, want %q", got, "PackageMismatch")
	}
	if got := replyErrorMessage(real); got != "installed name does not match target.packageName" {
		t.Errorf("replyErrorMessage(real) = %q, want %q", got, "installed name does not match target.packageName")
	}
}

func TestApplyInstallRequestID(t *testing.T) {
	// The observed receipt names the actual commit, so it wins outright.
	observed := &pkgmgr.ApplyResult{
		Action: "install", PackageName: "alpha", ToVersion: "1.2.0",
		InstallRequestID: "req-observed-alpha",
	}
	if got := applyInstallRequestID(observed); got != "req-observed-alpha" {
		t.Errorf("with a receipt = %q, want the observed pointer", got)
	}
	// Without one there is only the reconstruction, which cannot tell this
	// apply from any other write at the same package name and version.
	none := &pkgmgr.ApplyResult{Action: "skip", PackageName: "alpha", ToVersion: "1.2.0"}
	if got := applyInstallRequestID(none); got != "skip:alpha@1.2.0" {
		t.Errorf("without a receipt = %q, want the composed fallback", got)
	}
}

func TestInstallReceiptEnvelope(t *testing.T) {
	env, err := installReceiptEnvelope("vtx.identity.actor", "PROP1", "vtx.package.alpha", "req-observed")
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	if env.OperationType != "RecordCapabilityInstallReceipt" {
		t.Errorf("operationType = %q", env.OperationType)
	}
	if env.Lane != processor.LaneDefault {
		t.Errorf("lane = %q, want the default lane the mark-applied close also rides", env.Lane)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	want := map[string]any{
		"proposalId":       "PROP1",
		"packageKey":       "vtx.package.alpha",
		"installRequestId": "req-observed",
	}
	for k, v := range want {
		if payload[k] != v {
			t.Errorf("payload[%s] = %v, want %v", k, payload[k], v)
		}
	}
	if len(payload) != len(want) {
		t.Errorf("payload = %+v, want exactly the three declared fields", payload)
	}
	// The op hydrates from exactly these four keys — a dispatcher declaring a
	// different set fails the whole op on a hydration miss, so the set is
	// asserted here rather than trusted.
	wantReads := []string{
		"vtx.capabilityproposal.PROP1.review",
		"vtx.capabilityproposal.PROP1.target",
		"vtx.package.alpha",
		"vtx.package.alpha.manifest",
	}
	if env.ContextHint == nil || strings.Join(env.ContextHint.Reads, ",") != strings.Join(wantReads, ",") {
		t.Errorf("declared reads = %+v, want %v", env.ContextHint, wantReads)
	}
}

// A retry of the same close must derive the SAME requestId so the Contract #4
// tracker collapses it. A minted one would reach the commit batch instead,
// where .install's create-only conditioning refuses it — and the CLI would then
// log "the receipt did not land" while a valid receipt sits in KV.
func TestInstallReceiptEnvelope_RequestIDIsDerivedAndStable(t *testing.T) {
	first, err := installReceiptEnvelope("vtx.identity.actor", "PROP1", "vtx.package.alpha", "req-observed")
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	retry, err := installReceiptEnvelope("vtx.identity.actor", "PROP1", "vtx.package.alpha", "req-observed")
	if err != nil {
		t.Fatalf("rebuild envelope: %v", err)
	}
	if first.RequestID != retry.RequestID {
		t.Errorf("retry requestId = %q, want the first submit's %q", retry.RequestID, first.RequestID)
	}
	if !keys.IsValidNanoID(first.RequestID) {
		t.Errorf("requestId %q is not a valid Contract #1 NanoID, so the envelope is rejected before it is tracked", first.RequestID)
	}
	// The paired vector: a receipt naming a DIFFERENT package is a different
	// receipt and must NOT collapse into the first — create-only conditioning
	// is what arbitrates that, and it only gets to if the ids differ.
	other, err := installReceiptEnvelope("vtx.identity.actor", "PROP1", "vtx.package.beta", "req-observed")
	if err != nil {
		t.Fatalf("build the other envelope: %v", err)
	}
	if other.RequestID == first.RequestID {
		t.Errorf("a receipt naming another package derived the same requestId %q, so it would dedup instead of being refused", other.RequestID)
	}
}

// receiptApplyResult is the shape ApplyCapabilityPlan returns from an arm that
// actually committed.
func receiptApplyResult() *pkgmgr.ApplyResult {
	return &pkgmgr.ApplyResult{
		PackageName: "alpha", PackageKey: "vtx.package.alpha",
		Action: "install", ToVersion: "1.2.0", InstallRequestID: "req-observed",
	}
}

// stubOpResponder stands in for the Processor on the default lane: it records
// every envelope published to ops.default, in publish order, and answers each
// on the reply inbox the submitter put in the message header. Without a
// responder the submitter's reply wait is what ends the call, and the only way
// to end it is a deadline — a wall-clock guess in place of the thing actually
// being waited for. This waits for the real event instead.
func stubOpResponder(t *testing.T, conn *substrate.Conn, replyFor func(processor.OperationEnvelope) processor.OperationReply) (recorded func() []processor.OperationEnvelope) {
	t.Helper()
	var mu sync.Mutex
	var seen []processor.OperationEnvelope
	sub, err := conn.NATS().Subscribe("ops.default", func(m *nats.Msg) {
		var env processor.OperationEnvelope
		if json.Unmarshal(m.Data, &env) != nil {
			return
		}
		mu.Lock()
		seen = append(seen, env)
		mu.Unlock()
		inbox := m.Header.Get("Lattice-Reply-Inbox")
		if inbox == "" {
			return
		}
		raw, err := json.Marshal(replyFor(env))
		if err != nil {
			t.Errorf("marshal stub reply: %v", err)
			return
		}
		if err := conn.NATS().Publish(inbox, raw); err != nil {
			t.Errorf("publish stub reply: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("subscribe ops.default: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	// Flush before returning so the subscription is registered on the server
	// before the caller's first publish — otherwise the first op can be
	// published into a subject nothing is listening on yet.
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush the responder subscription: %v", err)
	}
	return func() []processor.OperationEnvelope {
		mu.Lock()
		defer mu.Unlock()
		return append([]processor.OperationEnvelope(nil), seen...)
	}
}

// acceptEveryOp is the stub reply for a close where both submits commit.
func acceptEveryOp(processor.OperationEnvelope) processor.OperationReply {
	return processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"}
}

// The order is the assertion. RecordCapabilityInstallReceipt requires the
// proposal to still be `approved`, and MarkCapabilityProposalApplied is exactly
// what flips it to `applied` — so a receipt submitted second is refused every
// time, and .install being create-only makes that refusal permanent.
func TestCloseApplyProposal_SubmitsReceiptBeforeMarkApplied(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorded := stubOpResponder(t, conn, acceptEveryOp)

	if err := closeApplyProposal(ctx, conn, bootstrap.BootstrapIdentityKey, "PROP1", receiptApplyResult(), logger); err != nil {
		t.Fatalf("closeApplyProposal: %v", err)
	}

	envs := recorded()
	if len(envs) != 2 {
		t.Fatalf("published %d default-lane ops, want 2: %+v", len(envs), envs)
	}
	if envs[0].OperationType != "RecordCapabilityInstallReceipt" || envs[1].OperationType != "MarkCapabilityProposalApplied" {
		t.Fatalf("published order = [%s, %s], want the receipt first", envs[0].OperationType, envs[1].OperationType)
	}
	// Both close ops run the same guards over the same four keys — the package
	// ROOT as well as its manifest — and each hydrates from its declared set
	// alone, so a short set fails the whole op on a hydration miss.
	wantReads := []string{
		"vtx.capabilityproposal.PROP1.review",
		"vtx.capabilityproposal.PROP1.target",
		"vtx.package.alpha",
		"vtx.package.alpha.manifest",
	}
	for _, env := range envs {
		if env.ContextHint == nil || strings.Join(env.ContextHint.Reads, ",") != strings.Join(wantReads, ",") {
			t.Errorf("%s reads = %+v, want %v", env.OperationType, env.ContextHint, wantReads)
		}
	}
}

// An apply arm that committed nothing has no install to bind, so submitting a
// receipt would record a fiction — only the mark-applied close may reach the
// lane. Paired with the ordering test above, which is its positive vector: the
// same close DOES publish the receipt when the apply committed one.
func TestCloseApplyProposal_SkipsReceiptWithoutObservedInstall(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorded := stubOpResponder(t, conn, acceptEveryOp)

	skipped := receiptApplyResult()
	skipped.Action, skipped.Skipped, skipped.InstallRequestID = "skip", true, ""
	if err := closeApplyProposal(ctx, conn, bootstrap.BootstrapIdentityKey, "PROP1", skipped, logger); err != nil {
		t.Fatalf("closeApplyProposal: %v", err)
	}

	envs := recorded()
	if len(envs) != 1 {
		t.Fatalf("published %d default-lane ops, want the mark-applied submit alone: %+v", len(envs), envs)
	}
	if envs[0].OperationType != "MarkCapabilityProposalApplied" {
		t.Errorf("published %q, want only the mark-applied close", envs[0].OperationType)
	}
}

// A refused receipt must not fail the command: the package is live and the
// proposal is still closable, so the close carries on to mark-applied and the
// verb succeeds. Paired with the rejection of mark-applied itself, which DOES
// fail — otherwise "no error" would pass for a close that refuses nothing.
func TestCloseApplyProposal_ReceiptRejectionIsNonFatal(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorded := stubOpResponder(t, conn, func(env processor.OperationEnvelope) processor.OperationReply {
		if env.OperationType == "RecordCapabilityInstallReceipt" {
			return processor.OperationReply{
				Status: processor.ReplyStatusRejected,
				Error:  &processor.ReplyError{Code: "UnknownPackage", Message: "vtx.package.alpha is not a live installed package"},
			}
		}
		return acceptEveryOp(env)
	})

	if err := closeApplyProposal(ctx, conn, bootstrap.BootstrapIdentityKey, "PROP1", receiptApplyResult(), logger); err != nil {
		t.Fatalf("a refused receipt failed the whole apply-proposal: %v", err)
	}
	if envs := recorded(); len(envs) != 2 || envs[1].OperationType != "MarkCapabilityProposalApplied" {
		t.Fatalf("published %+v, want the close to carry on to mark-applied", envs)
	}
}

func TestCloseApplyProposal_MarkAppliedRejectionFails(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stubOpResponder(t, conn, func(env processor.OperationEnvelope) processor.OperationReply {
		if env.OperationType == "MarkCapabilityProposalApplied" {
			return processor.OperationReply{
				Status: processor.ReplyStatusRejected,
				Error:  &processor.ReplyError{Code: "InvalidApplyTransition", Message: "proposal is not approved"},
			}
		}
		return acceptEveryOp(env)
	})

	err := closeApplyProposal(ctx, conn, bootstrap.BootstrapIdentityKey, "PROP1", receiptApplyResult(), logger)
	if err == nil {
		t.Fatal("a refused mark-applied reported success")
	}
	if !strings.Contains(err.Error(), "proposal is not approved") {
		t.Errorf("error = %v, want the rejection reason surfaced", err)
	}
}
