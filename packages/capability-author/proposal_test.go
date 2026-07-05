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
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	capabilityauthor "github.com/asolgan/lattice/packages/capability-author"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
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

func (fullCypherParser) Parse(ruleBody string) error {
	_, err := full.New().Parse(ruleBody)
	return err
}

// staffCapDoc grants the staff actor RequestCapabilityAuthoring +
// CreateAuthoringClaim + RecordCapabilityProposal + ReviewCapabilityProposal —
// modeled here as an operator-equivalent staff actor standing in for the
// human requester, Loom's relay actor, and the human reviewer, mirroring
// augur's staffCapDoc.
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
			{OperationType: "RecordCapabilityProposal", Scope: "any"},
			{OperationType: "ReviewCapabilityProposal", Scope: "any"},
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
		Actor:         capStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "capabilityauthorclaim",
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
	report, err := pkgmgr.ValidateCapabilityArtifact(kind, content, fullCypherParser{})
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
	capIDPending = "CAcapPendingHJKMNPQR"
	capIDBadKind = "CAcapBadKindHJKMNPQR"
	capIDBadConf = "CAcapBadConfHJKMNPQR"
	capIDBadSpec = "CAcapBadSpecHJKMNPQR"
	capIDNoClaim = "CAcapNoreqHJKMNPQRST"
	capIDReplay  = "CAcapRedoHJKMNPQRSTU"

	capHandlePending = "CAHNDPendingHJKMNPQR"
	capHandleBadKind = "CAHNDBadKindHJKMNPQR"
	capHandleBadConf = "CAHNDBadConfHJKMNPQR"
	capHandleBadSpec = "CAHNDBadSpecHJKMNPQR"
	capHandleReplay  = "CAHNDRedoHJKMNPQRSTU"
)

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
}

// TestCapAuthor_DisabledKind_Invalid: a kind outside this increment's enabled
// set is stored invalid — the proposal is still recorded (auditability), never
// pending.
func TestCapAuthor_DisabledKind_Invalid(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-badkind")

	proposalKey := "vtx.capabilityproposal." + capIDBadKind
	req := requestEnv(testutil.GenReqID("CARequest"), capIDBadKind, "grant RescheduleAppointment to front-desk")
	testutil.PublishOp(t, conn, req)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	claim := claimEnv(testutil.GenReqID("CAClaim"), capHandleBadKind, proposalKey)
	testutil.PublishOp(t, conn, claim)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	rec := recordEnv(t, testutil.GenReqID("CARecord"), capHandleBadKind, "grant", json.RawMessage(`{}`), 0.9)
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
