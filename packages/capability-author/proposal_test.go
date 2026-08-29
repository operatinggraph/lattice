// AI-authored-capabilities Fire 1 capture + dispatch integration tests —
// exercised end-to-end through the real Processor across the
// RequestCapabilityAuthoring → CreateAuthoringClaim → RecordCapabilityProposal
// flow.
//
// RequestCapabilityAuthoring mints the proposal vertex write-ahead with the
// requester + intent; CreateAuthoringClaim (the externalTask instanceOp the
// capabilityAuthor Loom pattern submits) mints the correlation-claim vertex
// the bridge's reply resolves through; RecordCapabilityProposal carries a
// proposed artifact + its ALREADY-COMPUTED §5 deterministic-validation
// verdict (computed here via pkgmgr.ValidateCapabilityArtifact, exactly as
// the bridge will in the full design) and stores review.state =
// pending | invalid. The tests prove: a validated lens artifact → pending; a
// disabled kind / out-of-range confidence / a validator-rejected artifact →
// invalid (auditable, never dispatchable); a record against an externalRef
// with no live claim is rejected (a proposal can never be resolved, let alone
// fabricated, with no claim).
//
// These tests live in an external test package (capabilityauthor_test) so they
// exercise the public Lattice surface a real Capability Package sees: seed the
// kernel, install the dependency chain + orchestration-base + capability-author
// through the Processor, then submit the ops and assert outcomes.
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
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

const (
	capStaffActorID  = "BBcapAuthActHJKMNPQR"
	capStaffActorKey = "vtx.identity." + capStaffActorID
	capStaffCapKey   = "cap.identity." + capStaffActorID
)

// fullCypherParser adapts ruleengine/full.Engine to pkgmgr.CypherParser for
// these tests — the same trusted-caller role the bridge plays in the full
// design (compute the §5 verdict BEFORE submitting RecordCapabilityProposal).
type fullCypherParser struct{}

func (fullCypherParser) Parse(ruleBody string) (pkgmgr.SpecLabels, error) {
	facts, err := full.SpecLabels(ruleBody)
	if err != nil {
		return pkgmgr.SpecLabels{}, err
	}
	return pkgmgr.SpecLabels{
		Referenced: facts.Referenced,
		Exhaustive: facts.Exhaustive,
		Expansion:  facts.Expansion,
	}, nil
}

// staffCapDoc grants the staff actor every one of the package's ops — modeled
// here as an operator-equivalent staff actor standing in for the human
// requester, Loom's relay actor, the human artifact author, the human reviewer,
// and the applying operator, mirroring augur's staffCapDoc.
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    capStaffCapKey,
		Actor:                  capStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{capStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RequestCapabilityAuthoring", Scope: "any"},
			{OperationType: "CreateAuthoringClaim", Scope: "any"},
			{OperationType: "SubmitCapabilityProposal", Scope: "any"},
			{OperationType: "RecordCapabilityProposal", Scope: "any"},
			{OperationType: "RecordAuthoringDispatch", Scope: "any"},
			{OperationType: "ReviewCapabilityProposal", Scope: "any"},
			{OperationType: "MarkCapabilityProposalApplied", Scope: "any"},
			{OperationType: "RecordCapabilityInstallReceipt", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// loomCapDoc grants Loom's primordial relay actor CreateAuthoringClaim — the
// externalTask instanceOp whose script pins op.actor to
// `primordialActor["loom"]`. Read through a func, not a package var:
// bootstrap's primordial globals are populated by SetupPackageTestEnv's
// EnsurePrimordials, well after package var initialization.
//
// staffCapDoc keeps its own CreateAuthoringClaim grant deliberately — an
// operator-role holder that is NOT Loom is exactly the forged-submitter vector
// the guard rejects, and the negative test submits as capStaffActorKey to prove
// the refusal comes from the actor check rather than from a missing grant.
func loomCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.LoomIdentityID,
		Actor:                  bootstrap.LoomIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.LoomIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAuthoringClaim", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupCapAuthorEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	installPkg(t, ctx, conn, orchestrationbase.Package)
	installPkg(t, ctx, conn, capabilityauthor.Package)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, loomCapDoc())
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

func newCapAuthorPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ca-" + durable,
	})
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

// findEmittedEvent reads the committed transactional-outbox aspect for an op's
// requestId and returns the payload of the first event of the given class.
// The outbox aspect is the faithful EventList persisted in the step-8 atomic
// batch (the outbox consumer publishes from it) — reading it asserts the
// emission without running the outbox consumer in the test harness. Mirrors
// packages/lease-signing's own helper of the same name.
func findEmittedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) map[string]any {
	t.Helper()
	outboxKey := processor.OutboxAspectKey(requestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == class {
			return e.Payload
		}
	}
	t.Fatalf("no %s event emitted by op %s (events: %v)", class, requestID, eventClasses(ob.Data.Events))
	return nil
}

func eventClasses(evs processor.EventList) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.EventType)
	}
	return out
}

func requestEnv(reqID, proposalID, intent string) *processor.OperationEnvelope {
	payload := map[string]any{"proposalId": proposalID, "intent": intent}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RequestCapabilityAuthoring",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// claimEnv builds the CreateAuthoringClaim payload the capabilityAuthor Loom
// pattern's externalTask step submits — subject-templated params exactly as
// packages/capability-author/patterns.go declares them, proving
// orchestration-base's resolve_subject_params resolution against the
// subject's own .request aspect end-to-end.
func claimEnv(reqID, handle, proposalKey string) *processor.OperationEnvelope {
	payload := map[string]any{
		"instanceKey": handle,
		"subjectKey":  proposalKey,
		"adapter":     "capabilityAuthor",
		"replyOp":     "RecordCapabilityProposal",
		"params": map[string]any{
			"requesterId": "subject.request.data.requesterId",
			"intent":      "subject.request.data.intent",
			"contextRef":  "subject.request.data.contextRef",
		},
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAuthoringClaim",
		Actor:         bootstrap.LoomIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityauthorclaim",
		Payload:       json.RawMessage(b),
		// Loom's own read inference (inferExternalTaskReads,
		// internal/loom/externaltask_params.go): the subject root is a
		// required Read, and every subject.<aspect>.data.<field> template
		// contributes the aspect key to EgressReads, never Reads.
		ContextHint: &processor.ContextHint{Reads: []string{proposalKey}, EgressReads: []string{proposalKey + ".request"}},
	}
}

// dispatchEnv builds the RecordAuthoringDispatch payload the bridge submits
// when its capabilityAuthor adapter returns Pending — the same six-field
// shape internal/bridge/dispatch.go's handlePending posts against every
// dispatchOp: {externalRef, vendorRef, adapter, replyOp, nextPollAt,
// deadline}, keyed on the CLAIM HANDLE a prior CreateAuthoringClaim minted.
func dispatchEnv(reqID, handle, vendorRef string) *processor.OperationEnvelope {
	payload := map[string]any{
		"externalRef": handle,
		"vendorRef":   vendorRef,
		"adapter":     "capabilityAuthor",
		"replyOp":     "RecordCapabilityProposal",
		"nextPollAt":  "2026-08-21T10:00:30Z",
		"deadline":    "2026-08-21T10:05:00Z",
	}
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordAuthoringDispatch",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityAuthorClaimDispatch",
		Payload:       json.RawMessage(b),
	}
}

// recordEnv builds the RecordCapabilityProposal payload in the standard
// bridge replyOp shape {externalRef, status, result} — externalRef is the
// CLAIM HANDLE a prior CreateAuthoringClaim minted (never the proposal's own
// id — the op resolves the real proposal via the claim's .target aspect).
// Running the §5 materializer HERE (the caller — exactly as the bridge will
// in the full design) before JSON-encoding its verdict into the result blob
// exactly as a real completed adapter reply would.
func recordEnv(t *testing.T, reqID, handle, kind string, content json.RawMessage, confidence float64) *processor.OperationEnvelope {
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
	result := map[string]any{
		"kind":       kind,
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "reasoned capability authoring proposal",
		"confidence": confidence,
		"validation": validation,
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
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.capabilityauthorclaim." + handle + ".target"}},
	}
}

// failedRecordEnv builds a RecordCapabilityProposal payload for the bridge's
// terminal OutcomeFailed leg: status=failed, result = the real adapter's own
// plain-text Detail string (internal/bridge/capability_author.go's
// terminalFailure/refusalDetail — never JSON, unlike the completed leg).
// Exercises the honest-failure-reason fix: the op must thread this string
// through verbatim as BOTH .rationale.text and review.invalidReason, never
// substitute a hardcoded refusal claim.
func failedRecordEnv(reqID, handle, resultDetail string) *processor.OperationEnvelope {
	payload := map[string]any{
		"externalRef": handle,
		"status":      "failed",
		"result":      resultDetail,
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
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.capabilityauthorclaim." + handle + ".target"}},
	}
}

func validLensContent(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: name,
		Adapter:       "nats-kv",
		Bucket:        "active-" + name,
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	return b
}

// validVertexTypeDDLScript is the minimal well-formed Starlark script a
// vertexTypeDDL artifact's record-time sandbox dry-run (starlarksandbox.
// Validate — internal/pkgmgr's first caller at package-install/record time,
// ai-authored-capabilities-design.md §8 Fire 4) accepts: it compiles and
// defines a 2-parameter execute(state, op) entrypoint.
const validVertexTypeDDLScript = "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n"

func validVertexTypeDDLContent(t *testing.T, canonicalName string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(pkgmgr.VertexTypeDDLArtifactContent{
		CanonicalName:     canonicalName,
		PermittedCommands: []string{"CreateWidget"},
		Description:       "an AI-authored widget",
		Script:            validVertexTypeDDLScript,
	})
	if err != nil {
		t.Fatalf("marshal vertexTypeDDL content: %v", err)
	}
	return b
}

func validOpMetaContent(t *testing.T, operationType string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(pkgmgr.OpMetaArtifactContent{
		OperationType: operationType,
		Presentation:  &pkgmgr.OpPresentationArtifact{Title: "Request a widget", Tone: "primary"},
	})
	if err != nil {
		t.Fatalf("marshal opMeta content: %v", err)
	}
	return b
}

func reviewState(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".review")
	data, _ := doc["data"].(map[string]any)
	s, _ := data["state"].(string)
	return s
}

// Per-scenario proposal ids + claim handles. Each is a valid 20-char bare
// NanoID. The handle is deliberately a DIFFERENT id than the proposal (as a
// real Loom-minted instanceKey always is — Contract #10 §10.3/§10.5) so the
// tests exercise the claim indirection, not an accidental id coincidence.
const (
	capIDPending        = "CAcapPendingHJKMNPQR"
	capIDBadKind        = "CAcapBadKindHJKMNPQR"
	capIDBadConf        = "CAcapBadConfHJKMNPQR"
	capIDBadSpec        = "CAcapBadSpecHJKMNPQR"
	capIDNoClaim        = "CAcapNoreqHJKMNPQRST"
	capIDReplay         = "CAcapRedoHJKMNPQRSTU"
	capIDGrantOverscope = "CAcapExceedHJKMNPQRS"
	capIDVertexTypeDDL  = "CAcapVertexDHJKMNPQR"
	capIDOpMeta         = "CAcapMetaXHJKMNPQRST"
	capIDForgedClaim    = "CAcapForgedHJKMNPQRS"
	capIDDispatch       = "CAcapDispatchHJKMNPQ"
	capIDRefused        = "CAcapRefusedHJKMNPQR"
	capIDTimeout        = "CAcapTimeoutHJKMNPQR"

	capHandlePending         = "CAHNDPendingHJKMNPQR"
	capHandleBadKind         = "CAHNDBadKindHJKMNPQR"
	capHandleBadConf         = "CAHNDBadConfHJKMNPQR"
	capHandleBadSpec         = "CAHNDBadSpecHJKMNPQR"
	capHandleReplay          = "CAHNDRedoHJKMNPQRSTU"
	capHandleGrantOverscope  = "CAHNDExceedHJKMNPQRS"
	capHandleVertexTypeDDL   = "CAHNDVertexDHJKMNPQR"
	capHandleOpMeta          = "CAHNDMetaXHJKMNPQRST"
	capHandleForgedClaim     = "CAHNDForgedHJKMNPQRS"
	capHandleDispatch        = "CAHNDDispatchHJKMNPQ"
	capHandleRefused         = "CAHNDRefusedHJKMNPQR"
	capHandleTimeout         = "CAHNDTimeoutHJKMNPQR"
	capHandleDispatchMissing = "CAHNDMissVendorRefHK"
)

// TestCapAuthor_ClaimByNonLoomOperator_Denied: the primordial actor guard.
// capStaffActorKey holds the operator role AND a Scope:"any"
// CreateAuthoringClaim grant, so step 3 authorizes it; only the script's
// `op.actor != primordialActor["loom"]` check stops it from having an arbitrary
// proposal's .request contents resolved into params and shipped to the
// capabilityAuthor vendor. Identical to the claim the passing tests commit,
// differing in the actor alone.
func TestCapAuthor_ClaimByNonLoomOperator_Denied(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-forged-claim")

	proposalKey := "vtx.capabilityproposal." + capIDForgedClaim
	req := requestEnv(testutil.GenReqID("CAReqForge"), capIDForgedClaim, "a lens a forger wants dispatched")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaimForge"), capHandleForgedClaim, proposalKey)
	claim.Actor = capStaffActorKey
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, claim)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a non-Loom operator's claim: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Loom's relay actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	// No claim vertex, and the proposal keeps no .claim aspect: a denied
	// instanceOp leaves the escalation entirely undispatched.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "vtx.capabilityauthorclaim."+capHandleForgedClaim); err == nil {
		t.Fatalf("a denied claim op must mint NO claim vertex")
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey+".claim"); err == nil {
		t.Fatalf("a denied claim op must write NO .claim aspect on the proposal")
	}
}

// TestCapAuthor_ValidLens_Pending: a well-formed, deterministically-validated
// lens artifact is stored review.state=pending (the fire's remaining
// increments make it dispatchable via review + apply).
func TestCapAuthor_ValidLens_Pending(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-pending")

	proposalKey := "vtx.capabilityproposal." + capIDPending
	req := requestEnv(testutil.GenReqID("CARequest"), capIDPending, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandlePending, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandlePending, "lens", validLensContent(t, "providersBySpecialty"), 0.86)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	// Root data is minimal (D5).
	root := readDoc(t, ctx, conn, proposalKey)
	if data, _ := root["data"].(map[string]any); len(data) != 0 {
		t.Fatalf("proposal root data must be {} (D5); got %v", data)
	}
	// The .request aspect carries the requester + intent (RequestCapabilityAuthoring).
	reqDoc := readDoc(t, ctx, conn, proposalKey+".request")
	rd, _ := reqDoc["data"].(map[string]any)
	if got, _ := rd["requesterId"].(string); got != capStaffActorKey {
		t.Fatalf(".request.requesterId = %q, want %q", got, capStaffActorKey)
	}
	// The .artifact aspect carries the proposed kind (RecordCapabilityProposal).
	artDoc := readDoc(t, ctx, conn, proposalKey+".artifact")
	ad, _ := artDoc["data"].(map[string]any)
	if got, _ := ad["kind"].(string); got != "lens" {
		t.Fatalf(".artifact.kind = %q, want lens", got)
	}
	// The requestedBy link: proposal is the source.
	lnk := readDoc(t, ctx, conn, "lnk.capabilityproposal."+capIDPending+".requestedBy.identity."+capStaffActorID)
	if got, _ := lnk["sourceVertex"].(string); got != proposalKey {
		t.Fatalf("requestedBy link sourceVertex = %q, want %q (proposal is source)", got, proposalKey)
	}
	// CreateAuthoringClaim wrote the .claim aspect on the PROPOSAL itself
	// (closing the capabilityAuthorPending lens's missing_authoring gap) and
	// the claim vertex's .target back-pointer resolves to this same proposal.
	claimDoc := readDoc(t, ctx, conn, proposalKey+".claim")
	cd, _ := claimDoc["data"].(map[string]any)
	if got, _ := cd["claimedAt"].(string); got == "" {
		t.Fatalf(".claim.claimedAt is empty, want a timestamp")
	}
	targetDoc := readDoc(t, ctx, conn, "vtx.capabilityauthorclaim."+capHandlePending+".target")
	td, _ := targetDoc["data"].(map[string]any)
	if got, _ := td["proposalKey"].(string); got != proposalKey {
		t.Fatalf("claim .target.proposalKey = %q, want %q", got, proposalKey)
	}
	// The dispatchOp seam (Increment 3): CreateAuthoringClaim's emitted
	// external.capabilityAuthor event names RecordAuthoringDispatch, so the
	// bridge's Pending path (internal/bridge/dispatch.go:293-298) is
	// dispatchable for this adapter.
	ev := findEmittedEvent(t, ctx, conn, claim.RequestID, "external.capabilityAuthor")
	if got, _ := ev["dispatchOp"].(string); got != "RecordAuthoringDispatch" {
		t.Fatalf("external.capabilityAuthor event dispatchOp = %q, want RecordAuthoringDispatch", got)
	}
}

// TestCapAuthor_RecordAuthoringDispatch_RecordsPendingMarker_NoCompletion: the
// bridge submits RecordAuthoringDispatch when its capabilityAuthor adapter
// returns Pending — payload {externalRef, vendorRef, adapter, replyOp,
// nextPollAt, deadline} with NO ContextHint.Reads (the bridge's actuator sets
// none, mirroring lease-signing's RecordServiceDispatch). It must commit
// read-free; the .dispatch aspect is written on the CLAIM vertex (root stays
// {} — D5); the reasoning call stays undone (no .artifact/.review write, only
// the capabilityAuthor.dispatchRecorded provenance event); and a second
// dispatch for the same handle is rejected by the create-only .dispatch guard.
func TestCapAuthor_RecordAuthoringDispatch_RecordsPendingMarker_NoCompletion(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-dispatch")

	proposalKey := "vtx.capabilityproposal." + capIDDispatch
	req := requestEnv(testutil.GenReqID("CADispReq"), capIDDispatch, "a lens a dispatch test wants pending")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CADispClaim"), capHandleDispatch, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claimKey := "vtx.capabilityauthorclaim." + capHandleDispatch
	vendorRef := "model-runner-ref-abc123"
	disp := dispatchEnv(testutil.GenReqID("CADispatch1"), capHandleDispatch, vendorRef)
	testutil.PublishOp(t, conn, disp)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the .dispatch aspect on the CLAIM vertex — {vendorRef, adapter,
	// replyOp, submittedAt, nextPollAt, deadline}.
	ddoc := readDoc(t, ctx, conn, claimKey+".dispatch")
	dd, _ := ddoc["data"].(map[string]any)
	if got, _ := dd["vendorRef"].(string); got != vendorRef {
		t.Fatalf("dispatch.vendorRef = %q, want %q", got, vendorRef)
	}
	if got, _ := dd["adapter"].(string); got != "capabilityAuthor" {
		t.Fatalf("dispatch.adapter = %q, want capabilityAuthor", got)
	}
	if got, _ := dd["replyOp"].(string); got != "RecordCapabilityProposal" {
		t.Fatalf("dispatch.replyOp = %q, want RecordCapabilityProposal", got)
	}
	if got, _ := dd["submittedAt"].(string); got == "" {
		t.Fatalf("dispatch.submittedAt is empty, want a canonical-UTC timestamp")
	}

	// (b) D5: the claim vertex root data stays {} (CreateAuthoringClaim minted
	// it {}; the dispatch op reconstructs the key read-free and never touches
	// the root).
	claimRoot := readDoc(t, ctx, conn, claimKey)
	if data, _ := claimRoot["data"].(map[string]any); len(data) != 0 {
		t.Fatalf("claim vertex root data must stay minimal ({}), got %v", data)
	}

	// (c) the reasoning call is NOT done: no .artifact aspect on the proposal
	// (only RecordCapabilityProposal writes one), and the .claim aspect
	// CreateAuthoringClaim wrote — the thing that actually closes
	// capabilityAuthorPending's missing_authoring gap (lenses.go:111-117,
	// which reads only .claim/.artifact) — is untouched by the dispatch.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey+".artifact"); err == nil {
		t.Fatalf("a pending dispatch must NOT write the proposal's .artifact aspect")
	}
	claimAspect := readDoc(t, ctx, conn, proposalKey+".claim")
	cd, _ := claimAspect["data"].(map[string]any)
	if got, _ := cd["claimedAt"].(string); got == "" {
		t.Fatalf("proposal .claim aspect must remain from CreateAuthoringClaim, got empty claimedAt")
	}

	// (d) only the capabilityAuthor.dispatchRecorded provenance event is
	// emitted (never a completion signal — the escalation is not done).
	prov := findEmittedEvent(t, ctx, conn, disp.RequestID, "capabilityAuthor.dispatchRecorded")
	if got, _ := prov["vendorRef"].(string); got != vendorRef {
		t.Fatalf("capabilityAuthor.dispatchRecorded vendorRef = %q, want %q", got, vendorRef)
	}
	if got, _ := prov["claimKey"].(string); got != claimKey {
		t.Fatalf("capabilityAuthor.dispatchRecorded claimKey = %q, want %q", got, claimKey)
	}

	// (e) a second dispatch for the same handle is rejected by the create-only
	// .dispatch conflict (the once-only guarantee at the DDL layer). The
	// bridge submits no Reads (mirrored here), so the rejection is the batch
	// conflict on the already-existing .dispatch key.
	disp2 := dispatchEnv(testutil.GenReqID("CADispatch2"), capHandleDispatch, "model-runner-ref-def456")
	testutil.PublishOp(t, conn, disp2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCapAuthor_RecordAuthoringDispatch_VendorRefRequired_Rejected: vendorRef
// is REQUIRED (mirrors lease-signing's RecordServiceDispatch). A dispatch
// with no vendorRef is rejected (InvalidArgument), read-free, and writes no
// .dispatch aspect.
func TestCapAuthor_RecordAuthoringDispatch_VendorRefRequired_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-dispatch-vendorref-required")

	handle := capHandleDispatchMissing
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CADispMiss1"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordAuthoringDispatch",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityAuthorClaimDispatch",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, "vtx.capabilityauthorclaim."+handle+".dispatch"); err == nil {
		t.Fatalf("a rejected dispatch must not write the .dispatch aspect")
	}
}

// TestCapAuthor_ValidVertexTypeDDL_Pending proves the Fire 4 "vertexTypeDDL"
// kind is actually enabled on the LIVE RecordCapabilityProposal Starlark op
// (packages/capability-author/ddls.go's ENABLED_KINDS), not just the Go-side
// pkgmgr.EnabledArtifactKinds map — the two allow-lists could otherwise drift
// silently (Go accepts a kind the Starlark op still rejects as disabled).
func TestCapAuthor_ValidVertexTypeDDL_Pending(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-vertextypeddl")

	proposalKey := "vtx.capabilityproposal." + capIDVertexTypeDDL
	req := requestEnv(testutil.GenReqID("CARequest"), capIDVertexTypeDDL, "a widget vertex type")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleVertexTypeDDL, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleVertexTypeDDL, "vertexTypeDDL", validVertexTypeDDLContent(t, "aiWidget"), 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	artDoc := readDoc(t, ctx, conn, proposalKey+".artifact")
	ad, _ := artDoc["data"].(map[string]any)
	if got, _ := ad["kind"].(string); got != "vertexTypeDDL" {
		t.Fatalf(".artifact.kind = %q, want vertexTypeDDL", got)
	}
}

// TestCapAuthor_ValidOpMeta_Pending is TestCapAuthor_ValidVertexTypeDDL_Pending's
// sibling proof for the Fire 4 "opMeta" kind.
func TestCapAuthor_ValidOpMeta_Pending(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-opmeta")

	proposalKey := "vtx.capabilityproposal." + capIDOpMeta
	req := requestEnv(testutil.GenReqID("CARequest"), capIDOpMeta, "an op-meta for requesting a widget")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleOpMeta, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleOpMeta, "opMeta", validOpMetaContent(t, "RequestWidget"), 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "pending" {
		t.Fatalf("review.state = %q, want pending", got)
	}
	artDoc := readDoc(t, ctx, conn, proposalKey+".artifact")
	ad, _ := artDoc["data"].(map[string]any)
	if got, _ := ad["kind"].(string); got != "opMeta" {
		t.Fatalf(".artifact.kind = %q, want opMeta", got)
	}
}

// TestCapAuthor_DisabledKind_Invalid: a kind outside the currently enabled
// set is stored invalid — the proposal is still recorded (auditability), never
// pending. weaverTarget is not enabled until Fire 3 (lens + grant are the two
// kinds enabled today).
func TestCapAuthor_DisabledKind_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-badkind")

	proposalKey := "vtx.capabilityproposal." + capIDBadKind
	req := requestEnv(testutil.GenReqID("CARequest"), capIDBadKind, "a convergence target over active leases")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleBadKind, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleBadKind, "weaverTarget", json.RawMessage(`{}`), 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (kind not enabled)", got)
	}
}

// TestCapAuthor_ConfidenceOutOfRange_Invalid: a confidence outside [0,1] stores
// the proposal invalid, even with an otherwise-valid artifact.
func TestCapAuthor_ConfidenceOutOfRange_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-badconf")

	proposalKey := "vtx.capabilityproposal." + capIDBadConf
	req := requestEnv(testutil.GenReqID("CARequest"), capIDBadConf, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleBadConf, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleBadConf, "lens", validLensContent(t, "overconfident"), 1.5)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (confidence out of range)", got)
	}
}

// TestCapAuthor_MaterializerRejected_Invalid: an artifact the §5 materializer
// itself rejects (unparseable cypher) is stored invalid — the record-time
// validation boundary is honored end-to-end through the real op.
func TestCapAuthor_MaterializerRejected_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-badspec")

	proposalKey := "vtx.capabilityproposal." + capIDBadSpec
	req := requestEnv(testutil.GenReqID("CARequest"), capIDBadSpec, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleBadSpec, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	badContent, err := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: "brokenLens",
		Adapter:       "nats-kv",
		Bucket:        "broken-lens",
		Spec:          "MATCH (p:provider RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleBadSpec, "lens", badContent, 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (materializer-rejected artifact)", got)
	}
}

// TestCapAuthor_GrantExceedsRequesterScope_Invalid: the adversarial case the
// "grant" kind's §5 scope check exists to close — the requester holds ONLY
// "self" for the named operationType, but the AI-authored artifact requests
// granting "any" (broader authority than the requester's own). Proves the
// scope check is genuinely wired end-to-end through the real op, not merely
// unit-tested in isolation: the proposal is recorded (auditable) but never
// pending, so it can never reach approve/apply.
func TestCapAuthor_GrantExceedsRequesterScope_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-grant-overscope")

	proposalKey := "vtx.capabilityproposal." + capIDGrantOverscope
	req := requestEnv(testutil.GenReqID("CAReqOverscope"), capIDGrantOverscope, "grant DeleteEverything to operator")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClmOverscope"), capHandleGrantOverscope, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	content, err := json.Marshal(pkgmgr.GrantArtifactContent{
		OperationType: "DeleteEverything",
		Scope:         "any",
		GrantsTo:      []string{"operator"},
	})
	if err != nil {
		t.Fatalf("marshal grant content: %v", err)
	}
	// The requester holds only "self" — narrower than the "any" the artifact
	// requests — exactly as pkgmgr.ValidateCapabilityArtifact's caller (the
	// bridge in the full design) would compute from a fresh Contract #6 read.
	held := []pkgmgr.HeldPermission{{OperationType: "DeleteEverything", Scope: "self"}}
	report, err := pkgmgr.ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held, nil)
	if err != nil {
		t.Fatalf("materializer error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected the materializer to reject a grant exceeding the requester's held scope, got valid")
	}
	validation := map[string]any{"state": "invalid"}
	if len(report.Errors) > 0 {
		b, _ := json.Marshal(report.Errors)
		validation["report"] = string(b)
	}
	result := map[string]any{
		"kind":       "grant",
		"content":    string(content),
		"target":     map[string]any{"mode": "newPackage"},
		"rationale":  "reasoned capability authoring proposal",
		"confidence": 0.9,
		"validation": validation,
	}
	resultBytes, _ := json.Marshal(result)
	payload := map[string]any{
		"externalRef": capHandleGrantOverscope,
		"status":      "completed",
		"result":      string(resultBytes),
	}
	b, _ := json.Marshal(payload)
	rec := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CARecOverscope"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordCapabilityProposal",
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.capabilityauthorclaim." + capHandleGrantOverscope + ".target"}},
	}
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, proposalKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid (grant exceeds requester's held scope — never dispatchable)", got)
	}
}

// TestCapAuthor_RecordFailed_HonestReason proves the honest-failure-reason
// fix: RecordCapabilityProposal's status=failed branch records the adapter's
// OWN result text as review.invalidReason (and .rationale.text) verbatim,
// never a hardcoded "model declined to propose (refusal)" claim. The real
// capabilityAuthor adapter (internal/bridge/capability_author.go's
// terminalFailure/refusalDetail) routes many distinct terminal causes through
// status=failed — a genuine vendor refusal is only one of them (a 24h
// timeout, a cold-episode-no-prompt restart, an unusable idempotencyKey, an
// empty intent, an invalid model-runner ack, and both model calls failing are
// the others) — so a reviewer must see the real cause, not a refusal claim
// that was never true (a down runner must never read as "the model refused
// you").
func TestCapAuthor_RecordFailed_HonestReason(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-failed-reason")

	// A genuine vendor refusal: the adapter's Detail legitimately says
	// "declined ... (refusal: ...)" because refusalDetail actually fired.
	refusedKey := "vtx.capabilityproposal." + capIDRefused
	req1 := requestEnv(testutil.GenReqID("CARefReq"), capIDRefused, "a lens the vendor's policy declines")
	testutil.PublishOp(t, conn, req1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	claim1 := claimEnv(testutil.GenReqID("CARefClaim"), capHandleRefused, refusedKey)
	testutil.PublishOp(t, conn, claim1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	refusalDetail := "capabilityAuthor: the model declined to propose (refusal: policy)"
	rec1 := failedRecordEnv(testutil.GenReqID("CARefRecord"), capHandleRefused, refusalDetail)
	testutil.PublishOp(t, conn, rec1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, refusedKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid", got)
	}
	reviewDoc := readDoc(t, ctx, conn, refusedKey+".review")
	rd, _ := reviewDoc["data"].(map[string]any)
	if got, _ := rd["invalidReason"].(string); got != refusalDetail {
		t.Fatalf("invalidReason = %q, want the adapter's own detail verbatim %q", got, refusalDetail)
	}
	rationaleDoc := readDoc(t, ctx, conn, refusedKey+".rationale")
	rat, _ := rationaleDoc["data"].(map[string]any)
	if got, _ := rat["text"].(string); got != refusalDetail {
		t.Fatalf(".rationale.text = %q, want the adapter's own detail verbatim %q", got, refusalDetail)
	}

	// A terminal failure that is NOT a refusal — e.g. both model calls timing
	// out. The op must NEVER substitute "declined (refusal)" for this: the
	// review queue has to tell the operator the truth (a down/slow runner,
	// not a policy decline).
	timeoutKey := "vtx.capabilityproposal." + capIDTimeout
	req2 := requestEnv(testutil.GenReqID("CATmoReq"), capIDTimeout, "a lens whose reasoning call times out")
	testutil.PublishOp(t, conn, req2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	claim2 := claimEnv(testutil.GenReqID("CATmoClaim"), capHandleTimeout, timeoutKey)
	testutil.PublishOp(t, conn, claim2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	timeoutDetail := "capabilityAuthor: both model calls failed (deadline exceeded; deadline exceeded)"
	rec2 := failedRecordEnv(testutil.GenReqID("CATmoRecord"), capHandleTimeout, timeoutDetail)
	testutil.PublishOp(t, conn, rec2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := reviewState(t, ctx, conn, timeoutKey); got != "invalid" {
		t.Fatalf("review.state = %q, want invalid", got)
	}
	timeoutReviewDoc := readDoc(t, ctx, conn, timeoutKey+".review")
	trd, _ := timeoutReviewDoc["data"].(map[string]any)
	got, _ := trd["invalidReason"].(string)
	if got != timeoutDetail {
		t.Fatalf("invalidReason = %q, want the adapter's own detail verbatim %q", got, timeoutDetail)
	}
	if strings.Contains(got, "declined") || strings.Contains(got, "refusal") {
		t.Fatalf("invalidReason %q falsely claims a refusal for a cause that was never one", got)
	}
}

// TestCapAuthor_RecordWithNoPriorRequest_Rejected: RecordCapabilityProposal
// against an externalRef with no prior CreateAuthoringClaim is rejected — a
// proposal can never be resolved (let alone fabricated) with no live claim
// (no-orphan, mirrors augur's UnknownAugurClaim).
func TestCapAuthor_RecordWithNoPriorRequest_Rejected(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-noclaim")

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capIDNoClaim, "lens", validLensContent(t, "orphan"), 0.9)
	testutil.PublishOp(t, conn, rec)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCapAuthor_RedeliveredRecord_Collapses: a redelivered RecordCapabilityProposal
// for an already-recorded proposal is rejected on replay (create-only .review
// conflicts), the idempotency backstop atop the Contract #4 tracker.
func TestCapAuthor_RedeliveredRecord_Collapses(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-replay")

	proposalKey := "vtx.capabilityproposal." + capIDReplay
	req := requestEnv(testutil.GenReqID("CARequest"), capIDReplay, "a lens listing active providers")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleReplay, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("CARecord")
	content := validLensContent(t, "replayed")
	rec1 := recordEnv(t, reqID, capHandleReplay, "lens", content, 0.8)
	testutil.PublishOp(t, conn, rec1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Same requestId redelivered: the Contract #4 tracker collapses it before
	// the DDL script even runs a second time.
	rec2 := recordEnv(t, reqID, capHandleReplay, "lens", content, 0.8)
	testutil.PublishOp(t, conn, rec2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeDuplicate)
}
