package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bridge"
	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The composition root's half of the capabilityAuthor wiring. internal/bridge's
// own tests prove the adapter's state machine against a SCRIPTED validator; that
// leaves one thing unproven, and it is the thing that decides whether an
// AI-authored target can ever be approved: whether the artifact the adapter
// assembles actually satisfies the REAL pkgmgr boundary this file injects. These
// tests run the adapter end to end with capabilityArtifactVerdict itself, so the
// two halves are joined by a test rather than by an assumption.

const (
	testCatalogBucket = "capability-author-context"
	testHandle        = "AuthClaimHandle12345"
	// staleLensNanoID is the installed lens's meta-vertex id — a valid 20-char
	// NanoID (Contract #1 alphabet). The adapter resolves the model's
	// canonicalName choice to exactly this and files it as the target's lensRef,
	// the only form the apply path resolves for a single-artifact target.
	staleLensNanoID    = "LensAAAAAAAAAAAAAAAA"
	staleLensCanonical = "staleOnboarding"
)

// fixtureRunner stands in for the model-runner fleet: it accepts every request
// and records the refs it was asked for.
type fixtureRunner struct {
	refs []string
}

func (f *fixtureRunner) Dispatch(_ context.Context, req wire.Request) (wire.Ack, error) {
	f.refs = append(f.refs, req.Ref)
	return wire.Ack{Status: wire.AckAccepted, Ref: req.Ref}, nil
}

// modelAnswer is the tool input a model returns under capabilityAuthorTool's
// schema — hand-written here because it is the wire shape, not something the
// adapter produces.
func modelAnswer(targetID string) string {
	return `{"kind":"weaverTarget",` +
		`"content":{"targetId":"` + targetID + `","lensRef":"` + staleLensCanonical + `",` +
		`"description":"Every identity whose onboarding has gone cold gets one reminder.",` +
		`"gaps":[{"gapColumn":"missing_reminder","action":"directOp","pattern":"","subject":"","adapter":"",` +
		`"operation":"SendReminder","assignee":"","target":"","params":[{"key":"identity","value":"row.key"}],` +
		`"reads":["row.key"],"issueCode":"","issueSeverity":""},` +
		`{"gapColumn":"missing_escalation","action":"surface","pattern":"","subject":"","adapter":"",` +
		`"operation":"","assignee":"","target":"","params":[],"reads":[],` +
		`"issueCode":"onboardingStalled","issueSeverity":"error"}]},` +
		`"rationale":"staleOnboarding already marks the cold rows.","confidence":0.81}`
}

// authorFixture wires the real validator to the real adapter over embedded NATS.
func authorFixture(t *testing.T) (*bridge.CapabilityAuthor, *fixtureRunner, *substrate.Conn) {
	t.Helper()
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap conn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// core-kv backs the proposal-vertex aspects the apply-plan test reads.
	for _, bucket := range []string{testCatalogBucket, wire.ResultsBucket, "core-kv"} {
		if _, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatalf("provision %s: %v", bucket, err)
		}
	}
	if !substrate.IsValidNanoID(staleLensNanoID) {
		t.Fatalf("staleLensNanoID %q is not a valid NanoID (fix the test constant)", staleLensNanoID)
	}
	row := `{"key":"vtx.meta.` + staleLensNanoID + `","class":"meta.lens","canonicalName":"` + staleLensCanonical + `",` +
		`"spec":{"spec":"MATCH (i:identity) RETURN i.key AS key, true AS missing_reminder"}}`
	if _, err := conn.KVPut(ctx, testCatalogBucket, "vtx.meta."+staleLensNanoID, []byte(row)); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	runner := &fixtureRunner{}
	adapter, err := bridge.NewCapabilityAuthor(runner, conn, testCatalogBucket, capabilityArtifactVerdict, pkgmgr.PlatformProtectedPackage)
	if err != nil {
		t.Fatalf("NewCapabilityAuthor: %v", err)
	}
	return adapter, runner, conn
}

func writeAnswer(t *testing.T, conn *substrate.Conn, ref, output string) {
	t.Helper()
	body, err := json.Marshal(wire.Result{
		State:  wire.StateCompleted,
		Ref:    ref,
		Output: json.RawMessage(output),
		Model:  "claude-opus-5-20260101",
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.KVPut(ctx, wire.ResultsBucket, ref, body); err != nil {
		t.Fatalf("write result: %v", err)
	}
}

func runAuthoring(t *testing.T, adapter *bridge.CapabilityAuthor, conn *substrate.Conn, refs *[]string, answer string) bridge.CapabilityAuthorProposal {
	t.Helper()
	ctx := context.Background()
	d, err := adapter.Execute(ctx, bridge.Request{
		IdempotencyKey: testHandle,
		Operation:      "RecordCapabilityProposal",
		Subject:        testHandle,
		Params:         map[string]string{"intent": "remind identities whose onboarding has gone cold"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d.Disposition != bridge.Pending {
		t.Fatalf("Execute = %+v, want Pending", d)
	}
	writeAnswer(t, conn, testHandle, answer)

	d, err = adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition == bridge.Pending {
		// The first draft failed validation and a correction pass went out;
		// answer it with the same draft so the episode reaches its final verdict.
		if len(*refs) < 2 {
			t.Fatalf("Poll returned Pending but no correction call was dispatched")
		}
		writeAnswer(t, conn, (*refs)[1], answer)
		d, err = adapter.Poll(ctx, testHandle)
		if err != nil {
			t.Fatalf("Poll (final): %v", err)
		}
	}
	if d.Disposition != bridge.Resolved || d.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("Poll = %+v, want a completed proposal", d)
	}
	var p bridge.CapabilityAuthorProposal
	if err := json.Unmarshal([]byte(d.Result.Detail), &p); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	return p
}

// TestCapabilityAuthorAssemblyPassesTheRealBoundary is the load-bearing one: an
// artifact the adapter assembled from a well-formed model answer must satisfy
// pkgmgr's own §5 check. If it did not, every AI-authored target would record as
// invalid and no amount of adapter-side testing would have said so.
func TestCapabilityAuthorAssemblyPassesTheRealBoundary(t *testing.T) {
	t.Parallel()
	adapter, runner, conn := authorFixture(t)

	p := runAuthoring(t, adapter, conn, &runner.refs, modelAnswer("coldOnboardingReminder"))

	if p.Validation.State != bridge.ValidationStateValid {
		t.Fatalf("validation.state = %q, want %q — report: %s\ncontent: %s",
			p.Validation.State, bridge.ValidationStateValid, p.Validation.Report, p.Content)
	}
	if p.Kind != bridge.CapabilityAuthorKind {
		t.Errorf("kind = %q, want %q", p.Kind, bridge.CapabilityAuthorKind)
	}
	if p.Target.Mode != "newPackage" || p.Target.PackageName == "" {
		t.Errorf("target = %+v, want newPackage into a fresh handle-derived package (the apply path rejects install/empty)", p.Target)
	}
	if len(runner.refs) != 1 {
		t.Errorf("dispatches = %d, want 1 — a valid first draft needs no correction pass", len(runner.refs))
	}
}

// TestCapabilityAuthorAppliesEndToEnd is the load-bearing apply proof: an
// assembled proposal must build a real apply plan, not merely pass the §5
// validator. pkgmgr.CapabilityApplyPlanForProposal is the exact call the review
// console's Apply runs, and it refuses an empty packageName, a mode other than
// newPackage/upgradeExisting, a platform-protected name, or a newPackage name
// already installed — every guard the Studio's own `{mode:"install"}` bundle
// trips. Reaching a plan proves mode + packageName + the NanoID lensRef are all
// apply-legal end to end.
func TestCapabilityAuthorAppliesEndToEnd(t *testing.T) {
	t.Parallel()
	adapter, runner, conn := authorFixture(t)
	ctx := context.Background()

	p := runAuthoring(t, adapter, conn, &runner.refs, modelAnswer("coldOnboardingReminder"))
	if p.Validation.State != bridge.ValidationStateValid {
		t.Fatalf("validation.state = %q, want valid — report: %s", p.Validation.State, p.Validation.Report)
	}

	// The recorded lensRef is the installed lens's NanoID, not the model's
	// canonicalName — the only form resolveLensRef passes through at install.
	var content map[string]any
	if err := json.Unmarshal([]byte(p.Content), &content); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if content["lensRef"] != staleLensNanoID {
		t.Fatalf("content.lensRef = %#v, want the resolved NanoID %q", content["lensRef"], staleLensNanoID)
	}

	// Materialize the proposal vertex exactly as RecordCapabilityProposal +
	// approve would leave it, then build the apply plan.
	proposalID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("mint proposal id: %v", err)
	}
	proposalKey := "vtx.capabilityproposal." + proposalID
	writeAspect(t, conn, proposalKey+".review", map[string]any{"state": "approved"})
	writeAspect(t, conn, proposalKey+".artifact", map[string]any{"kind": p.Kind, "content": p.Content})
	writeAspect(t, conn, proposalKey+".target", map[string]any{
		"packageName": p.Target.PackageName,
		"mode":        p.Target.Mode,
		"newVersion":  p.Target.NewVersion,
	})

	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: want a buildable plan, got %v", err)
	}
	if plan.PackageName != p.Target.PackageName {
		t.Errorf("plan.PackageName = %q, want %q", plan.PackageName, p.Target.PackageName)
	}
	if pkgmgr.PlatformProtectedPackage(plan.PackageName) {
		t.Errorf("plan.PackageName %q is platform-protected — an AI-authored name must never be", plan.PackageName)
	}
	materialized := plan.MaterializedDefinition()
	if len(materialized.WeaverTargets) != 1 {
		t.Fatalf("plan defines %d weaver targets, want 1", len(materialized.WeaverTargets))
	}
	if got := materialized.WeaverTargets[0].LensRef; got != staleLensNanoID {
		t.Errorf("plan target lensRef = %q, want the NanoID %q that install resolves", got, staleLensNanoID)
	}
}

// writeAspect stores a Contract #1 {data} aspect envelope in core-kv, the shape
// pkgmgr.CapabilityApplyPlanForProposal reads.
func writeAspect(t *testing.T, conn *substrate.Conn, key string, data map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatalf("marshal aspect %s: %v", key, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.KVPut(ctx, "core-kv", key, body); err != nil {
		t.Fatalf("write aspect %s: %v", key, err)
	}
}

// TestCapabilityAuthorRealBoundaryRejectsABadToken is the negative vector the
// positive one needs: the same pipeline, one rule broken, and the REAL validator
// — not the adapter's own assembly — is what catches it. Without this, a
// validator wired to something that always returns valid would pass the test
// above.
func TestCapabilityAuthorRealBoundaryRejectsABadToken(t *testing.T) {
	t.Parallel()
	adapter, runner, conn := authorFixture(t)

	p := runAuthoring(t, adapter, conn, &runner.refs, modelAnswer("cold.onboarding.reminder"))

	if p.Validation.State != bridge.ValidationStateInvalid {
		t.Fatalf("validation.state = %q, want %q — a dotted targetId is not a KV-key segment",
			p.Validation.State, bridge.ValidationStateInvalid)
	}
	if !strings.Contains(p.Validation.Report, "TargetID") {
		t.Errorf("validation.report = %q, want pkgmgr's own error text", p.Validation.Report)
	}
	if len(runner.refs) != 2 {
		t.Errorf("dispatches = %d, want 2 — an invalid draft buys exactly one correction pass", len(runner.refs))
	}
}

// TestCapabilityArtifactVerdictFailsClosed pins the error leg: pkgmgr returns an
// ERROR (not a verdict) for content it cannot decode at all, and an unknown
// verdict must never read as approval.
func TestCapabilityArtifactVerdictFailsClosed(t *testing.T) {
	t.Parallel()
	state, report := capabilityArtifactVerdict(bridge.CapabilityAuthorKind, []byte(`"not an object"`))
	if state != bridge.ValidationStateInvalid {
		t.Errorf("state = %q, want %q", state, bridge.ValidationStateInvalid)
	}
	if report == "" {
		t.Error("report is empty; the operator would see no reason")
	}
}

// TestCapabilityArtifactVerdictRejectsADisabledKind pins the kind allow-list:
// the adapter only ever asks about weaverTarget, so a kind outside the enabled
// set reaching the validator means something upstream went wrong, and it must
// fail rather than be waved through.
func TestCapabilityArtifactVerdictRejectsADisabledKind(t *testing.T) {
	t.Parallel()
	state, report := capabilityArtifactVerdict("somethingElse", []byte(`{}`))
	if state != bridge.ValidationStateInvalid {
		t.Errorf("state = %q, want %q", state, bridge.ValidationStateInvalid)
	}
	if !strings.Contains(report, "not enabled") {
		t.Errorf("report = %q, want the disabled-kind reason", report)
	}
}
