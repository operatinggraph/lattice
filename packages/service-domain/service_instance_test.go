// Service lifecycle integration tests + the D5 gate.
//
// These tests live in an external test package (servicedomain_test) so they
// exercise the public Lattice surface a real Capability Package sees: seed the
// kernel, install rbac-domain + identity-domain + orchestration-base +
// service-domain through the Processor, then submit the lifecycle ops and
// assert the committed Core-KV shape — the instance is a real linked vertex
// whose outcome lives in an aspect with root data minimal.
package servicedomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
)

const (
	svcStaffActorID  = "BBstaffActHJKMNPQRST"
	svcStaffActorKey = "vtx.identity." + svcStaffActorID
	svcStaffCapKey   = "cap.identity." + svcStaffActorID
)

// staffCapDoc grants the staff actor the three service lifecycle ops (scope any).
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    svcStaffCapKey,
		Actor:                  svcStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{svcStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateServiceTemplate", Scope: "any"},
			{OperationType: "CreateServiceInstance", Scope: "any"},
			{OperationType: "RecordServiceOutcome", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// setupServiceEnv seeds the kernel, installs the dependency chain +
// orchestration-base + service-domain, and seeds the staff cap doc.
func setupServiceEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+identity+hygiene
	installServiceDeps(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	return ctx, conn
}

// installServiceDeps installs orchestration-base then service-domain through
// the real meta-install pipeline.
func installServiceDeps(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, servicedomain.Package); err != nil {
		t.Fatalf("install service-domain: %v", err)
	}
}

func newServicePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "svc-" + durable,
	})
}

// nanoIDFromRequestID predicts the NanoID the DDL's first nanoid.new() mints
// (deterministic from the requestId — the same algorithm the Processor uses).
func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// seedVertex writes a minimal live vertex directly to Core KV.
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

// seedDeletedVertex writes a soft-deleted (isDeleted=true) vertex directly to
// Core KV: the key resolves (it is present + hydratable) but vertex_alive
// treats it as dead, so a link-write endpoint check rejects it.
func seedDeletedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": true, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed deleted vertex %s: %v", key, err)
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

// keyExists reports whether a live (non-tombstoned) doc exists at key.
func keyExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	if del, _ := doc["isDeleted"].(bool); del {
		return false
	}
	return true
}

// createTemplate submits CreateServiceTemplate and returns the template key.
func createTemplate(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, family string) string {
	t.Helper()
	reqID := testutil.GenReqID("tpl" + family)
	tplID := nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceTemplate",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"` + family + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.service." + tplID
}

// TestServiceInstance_OutcomeInAspect_RootMinimal is THE D5 GATE (AC #2,
// invariant b). It runs the full lifecycle through the real install +
// Processor pipeline, then reads back the COMMITTED instance and asserts:
//   - the instance root data is minimal ({} — the class discriminator is an
//     aspect, not root);
//   - the outcome (status + completedAt) lives in the .outcome ASPECT, never
//     on the root.
func TestServiceInstance_OutcomeInAspect_RootMinimal(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "d5-gate")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBapp1icantHJKMNPQRS"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	// CreateServiceInstance.
	instReqID := testutil.GenReqID("d5Instance001")
	instID := nanoIDFromRequestID(instReqID)
	instKey := "vtx.service." + instID
	instEnv := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey +
			`","providedTo":"` + applicantKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, instEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Before RecordServiceOutcome: the outcome aspect must be ABSENT
	// (absence = not-yet-complete; no pending outcome is written at create).
	if keyExists(t, ctx, conn, instKey+".outcome") {
		t.Fatalf("outcome aspect must not exist before RecordServiceOutcome")
	}

	// RecordServiceOutcome.
	completedAt := "2026-06-18T14:00:00Z"
	recEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("d5Record00001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed","completedAt":"` + completedAt + `"}`),
		// The .outcome aspect does not exist yet, so it is NOT listed in Reads
		// (a not-yet-written key is a hydration miss). The CreateOnly write is
		// the once-only guarantee.
		ContextHint: &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, recEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) The committed instance ROOT data is minimal ({}). The type/subtype
	// discriminator is the ENVELOPE class (P7 — service.<x>.instance), not root
	// data and not a .class shadow aspect.
	instDoc := readDoc(t, ctx, conn, instKey)
	if cls, _ := instDoc["class"].(string); cls != "service.backgroundCheck.instance" {
		t.Fatalf("instance root class = %q, want service.backgroundCheck.instance (P7 envelope class)", cls)
	}
	data, _ := instDoc["data"].(map[string]any)
	if len(data) != 0 {
		t.Fatalf("instance root data must be minimal ({}), got %v", data)
	}

	// (b) The OUTCOME lives in the .outcome aspect: status + completedAt are
	// THERE (and completedAt is the canonical-UTC value).
	outcomeDoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := outcomeDoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "completed" {
		t.Fatalf("outcome.status = %q, want completed", got)
	}
	if got, _ := odata["completedAt"].(string); got != completedAt {
		t.Fatalf("outcome.completedAt = %q, want %q", got, completedAt)
	}
	if vk, _ := outcomeDoc["vertexKey"].(string); vk != instKey {
		t.Fatalf("outcome aspect vertexKey = %q, want %q", vk, instKey)
	}

	// P7: there is NO .class shadow aspect — the discriminator is the envelope
	// class asserted above.
	if keyExists(t, ctx, conn, instKey+".class") {
		t.Fatalf("instance must carry NO .class shadow aspect (P7 — the envelope class is the discriminator)")
	}
}

// TestServiceInstance_LinksSentenceValid asserts the committed links exist with
// the right keys + directions (the service vertex is the source per Contract #1
// §1.1). The providedTo link (instance→identity) is the convergence link a
// downstream lens reads across.
func TestServiceInstance_LinksSentenceValid(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "links")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "payment")
	tplID := tplKey[len("vtx.service."):]

	applicantID := "BBpayapp1cntHJKMNPQR"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	instReqID := testutil.GenReqID("linkInstance01")
	instID := nanoIDFromRequestID(instReqID)
	instKey := "vtx.service." + instID
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"family":"payment","template":"` + tplKey +
			`","providedTo":"` + applicantKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// instanceOf: instance → template (both type service).
	instanceOfLnk := "lnk.service." + instID + ".instanceOf.service." + tplID
	ioDoc := readDoc(t, ctx, conn, instanceOfLnk)
	if got, _ := ioDoc["sourceVertex"].(string); got != instKey {
		t.Fatalf("instanceOf sourceVertex = %q, want %q (instance is source)", got, instKey)
	}
	if got, _ := ioDoc["targetVertex"].(string); got != tplKey {
		t.Fatalf("instanceOf targetVertex = %q, want %q (template is target)", got, tplKey)
	}

	// providedTo: instance → identity.
	providedToLnk := "lnk.service." + instID + ".providedTo.identity." + applicantID
	ptDoc := readDoc(t, ctx, conn, providedToLnk)
	if got, _ := ptDoc["sourceVertex"].(string); got != instKey {
		t.Fatalf("providedTo sourceVertex = %q, want %q (instance is source)", got, instKey)
	}
	if got, _ := ptDoc["targetVertex"].(string); got != applicantKey {
		t.Fatalf("providedTo targetVertex = %q, want %q (identity is target)", got, applicantKey)
	}
}

// TestServiceTemplate_OptionalEndpointLinks proves the template's providedBy
// link vocabulary: the create-template op writes it only when the endpoint is
// supplied, validated alive, and the link is sentence-valid (template is
// source). The availableAt availability assertion is owned by service-location,
// not this DDL.
func TestServiceTemplate_OptionalEndpointLinks(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "tpl-links")

	provID := "BBproviderrHJKMNPQRS"
	provKey := "vtx.identity." + provID
	seedVertex(t, ctx, conn, provKey, "identity", map[string]any{"state": "claimed"})

	reqID := testutil.GenReqID("tplEndpoints1")
	tplID := nanoIDFromRequestID(reqID)
	tplKey := "vtx.service." + tplID
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceTemplate",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","providedBy":"` + provKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{provKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	provLnk := "lnk.service." + tplID + ".providedBy.identity." + provID
	pdoc := readDoc(t, ctx, conn, provLnk)
	if got, _ := pdoc["sourceVertex"].(string); got != tplKey {
		t.Fatalf("providedBy sourceVertex = %q, want %q (template is source)", got, tplKey)
	}
}

// TestServiceTemplate_AbsentEndpoint_Rejected proves the no-orphan invariant on
// the optional template providedBy link: a supplied-but-absent endpoint is
// rejected (the link is never committed pointing at a non-existent vertex).
func TestServiceTemplate_AbsentEndpoint_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "tpl-orphan")

	missingProv := "vtx.identity.BBmissingprovHJKMNPQR"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("tplOrphan0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceTemplate",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","providedBy":"` + missingProv + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{missingProv}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestServiceInstance_CallerSuppliedId proves the forward-fit seam (§5): a
// caller-supplied bare-NanoID instanceId mints vtx.service.<thatId> verbatim,
// and a re-submit with the same requestId/id collapses (CreateOnly + Contract
// #4 tracker) — no duplicate.
func TestServiceInstance_CallerSuppliedId(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "caller-id")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBca11eridapp1cntHJK"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	// A Loom-style write-ahead bare handle.
	suppliedID := "WAhand1eHJKMNPQRSTUV"
	suppliedKey := "vtx.service." + suppliedID
	reqID := testutil.GenReqID("callerSupplied1")
	payload := json.RawMessage(`{"family":"backgroundCheck","instanceId":"` + suppliedID +
		`","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`)
	mkEnv := func() *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateServiceInstance",
			Actor:         svcStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "service",
			Payload:       payload,
			ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
		}
	}

	testutil.PublishOp(t, conn, mkEnv())
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The instance vertex is keyed verbatim by the supplied bare NanoID; the
	// discriminator is the ENVELOPE class (P7), with no .class shadow aspect.
	instDoc := readDoc(t, ctx, conn, suppliedKey)
	if cls, _ := instDoc["class"].(string); cls != "service.backgroundCheck.instance" {
		t.Fatalf("instance root class = %q, want service.backgroundCheck.instance (P7 envelope class)", cls)
	}
	if keyExists(t, ctx, conn, suppliedKey+".class") {
		t.Fatalf("instance must carry NO .class shadow aspect (P7)")
	}
	rev1 := readDoc(t, ctx, conn, suppliedKey)["lastModifiedByOp"]

	// Re-submit with the same requestId/id — collapses on the Contract #4
	// tracker (idempotent). The outcome is OutcomeDuplicate (the tracker is
	// already present) and no new commit lands, so the instance is unchanged.
	testutil.PublishOp(t, conn, mkEnv())
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeDuplicate)
	rev2 := readDoc(t, ctx, conn, suppliedKey)["lastModifiedByOp"]
	if rev1 != rev2 {
		t.Fatalf("re-submit with same requestId must collapse (no new commit): %v vs %v", rev1, rev2)
	}
}

// TestRecordServiceOutcome_UnknownInstance_Rejected: recording an outcome for a
// non-existent instance is rejected (structured ScriptError).
func TestRecordServiceOutcome_UnknownInstance_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "rec-unknown")

	missing := "vtx.service.BBmissinginstHJKMNPQ"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("recUnknown001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"instanceKey":"` + missing + `","status":"completed","completedAt":"2026-06-18T14:00:00Z"}`),
		// The instance does not exist; the root read alone is a hydration miss
		// (the absent instance is rejected before the outcome is written).
		ContextHint: &processor.ContextHint{Reads: []string{missing}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_OnTemplate_Rejected: recording an outcome on a
// template (not an instance) is a category error and is rejected.
func TestRecordServiceOutcome_OnTemplate_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "rec-template")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("recTemplate01"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"instanceKey":"` + tplKey + `","status":"completed","completedAt":"2026-06-18T14:00:00Z"}`),
		// The template is alive + hydratable but its .class ends in .template,
		// so the structured NotAnInstance guard fires. A template has no
		// .outcome aspect, so it is not listed.
		ContextHint: &processor.ContextHint{Reads: []string{tplKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_AlreadyRecorded_Rejected: recording an outcome twice
// is rejected (the outcome is recorded once; absence of the aspect is the
// once-only guard).
func TestRecordServiceOutcome_AlreadyRecorded_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "rec-twice")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "payment")

	applicantID := "BBtwiceapp1cntHJKMNP"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	instReqID := testutil.GenReqID("twiceInstance1")
	instID := nanoIDFromRequestID(instReqID)
	instKey := "vtx.service." + instID
	createEnv := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"payment","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, createEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	mkRec := func(label string, reads []string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "RecordServiceOutcome",
			Actor:         svcStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "service",
			Payload:       json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed","completedAt":"2026-06-18T14:00:00Z"}`),
			ContextHint:   &processor.ContextHint{Reads: reads},
		}
	}

	// First record succeeds (the .outcome aspect does not exist yet).
	testutil.PublishOp(t, conn, mkRec("twiceRec00001", []string{instKey}))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Second record (different requestId) lists the now-existing .outcome
	// aspect, so the explicit OutcomeAlreadyRecorded guard fires and rejects.
	testutil.PublishOp(t, conn, mkRec("twiceRec00002", []string{instKey, instKey + ".outcome"}))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_StaleRevision_Rejected proves the OCC guard: a
// RecordServiceOutcome carrying a stale expectedRevision for the instance root
// is rejected (Contract #2 §2.6).
func TestRecordServiceOutcome_StaleRevision_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "rec-occ")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBoccapp1cantHJKMNPQ"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	instReqID := testutil.GenReqID("occInstance001")
	instID := nanoIDFromRequestID(instReqID)
	instKey := "vtx.service." + instID
	createEnv := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, createEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A revision the instance root never had (it is at revision 1 fresh from
	// create). 99 asserts a stale/wrong revision → conflict.
	recEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("occRecord00001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload: json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed","completedAt":"2026-06-18T14:00:00Z","expectedRevision":99}`),
		// No .outcome yet; the stale expectedRevision=99 makes the OCC-guarded
		// root touch conflict and reject.
		ContextHint: &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, recEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Ensure the import is exercised so the dependency-order references resolve.
	_ = identitydomain.Package
}

// TestCreateServiceInstance_InstanceOfInstance_Rejected: CreateServiceInstance
// pointing its template endpoint at a vertex that is itself an INSTANCE (its
// .class ends in .instance, not .template) is rejected with NotATemplate — an
// instance is not a valid offering to be a run of.
func TestCreateServiceInstance_InstanceOfInstance_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "inst-of-inst")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBnottp1app1cntHJKMN"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	// Create a real instance to use (illegitimately) as a template endpoint.
	instReqID := testutil.GenReqID("notTplInst0001")
	realInstID := nanoIDFromRequestID(instReqID)
	realInstKey := "vtx.service." + realInstID
	createEnv := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, createEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Now try to create an instance whose template endpoint is that instance.
	// The instance's .class ends in .instance, so NotATemplate fires.
	badEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("notTplInst0002"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + realInstKey + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{realInstKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, badEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceInstance_DeletedApplicant_Rejected: CreateServiceInstance
// providedTo a soft-deleted (isDeleted) identity is rejected with
// UnknownApplicant — vertex_alive treats a tombstoned vertex as dead, so the
// no-orphan invariant (FR29 / P4) rejects the link.
func TestCreateServiceInstance_DeletedApplicant_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "dead-applicant")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	deadID := "BBdeadapp1cntHJKMNPQ"
	deadKey := "vtx.identity." + deadID
	seedDeletedVertex(t, ctx, conn, deadKey, "identity", map[string]any{"state": "claimed"})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("deadApplic0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey + `","providedTo":"` + deadKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, deadKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceInstance_NonExistentTemplate_Rejected: CreateServiceInstance
// instanceOf a key that does not exist at all is rejected with UnknownTemplate.
func TestCreateServiceInstance_NonExistentTemplate_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "nx-template")

	applicantID := "BBnxtp1app1cntHJKMNP"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	// The missing template is NOT listed in ContextHint.Reads — listing a
	// non-existent key would surface as a HydrationMiss before the script runs.
	// Omitting it lets the op reach the script, where vertex_alive(state,
	// template) is False (key absent from state) and the UnknownTemplate guard
	// is what rejects. Only the live applicant is hydrated.
	missingTpl := "vtx.service.BBmissingtp1HJKMNPQR"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("nxTemplate001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + missingTpl + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceInstance_NonExistentApplicant_Rejected: CreateServiceInstance
// providedTo a key that does not exist at all is rejected with UnknownApplicant.
func TestCreateServiceInstance_NonExistentApplicant_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "nx-applicant")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	// The missing applicant is NOT listed in ContextHint.Reads (a non-existent
	// listed key is a HydrationMiss before the script). Omitting it lets the op
	// reach the script: the template hydrates + validates, then vertex_alive(
	// state, providedTo) is False and the UnknownApplicant guard rejects.
	missingApplicant := "vtx.identity.BBmissingapp1HJKMNPQ"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("nxApplicant01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey + `","providedTo":"` + missingApplicant + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_StatusOutOfEnum_Rejected: a status outside
// {completed, failed} (e.g. "pending") is rejected with InvalidArgument.
func TestRecordServiceOutcome_StatusOutOfEnum_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "status-enum")

	instKey := createLiveInstance(t, ctx, conn, cp, cons, "BBenumapp1cntHJKMNPQ")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("statusEnum001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"instanceKey":"` + instKey + `","status":"pending","completedAt":"2026-06-18T14:00:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceTemplate_FamilyOutOfEnum_Rejected: a create op family outside
// {backgroundCheck, payment} (e.g. "inspection") is rejected with InvalidArgument.
func TestCreateServiceTemplate_FamilyOutOfEnum_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "family-enum")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("familyEnum001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceTemplate",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"inspection"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_CompletedAtMalformed_Rejected: a completedAt that is
// not a valid RFC3339 instant is rejected (time.rfc3339_utc raises a structured
// ScriptError).
func TestRecordServiceOutcome_CompletedAtMalformed_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "completedat-bad")

	instKey := createLiveInstance(t, ctx, conn, cp, cons, "BBbadtsapp1cntHJKMNP")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("badTs00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed","completedAt":"not-a-timestamp"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_CompletedAtAbsent_Rejected: an absent (empty)
// completedAt is rejected — required_string fails before the RFC3339 normalize.
func TestRecordServiceOutcome_CompletedAtAbsent_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "completedat-absent")

	instKey := createLiveInstance(t, ctx, conn, cp, cons, "BBnotsapp1cntHJKMNPQ")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("noTs000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateServiceInstance_IdCollision_DifferentRequestIds_Rejected proves the
// caller-supplied-id collision guard distinct from the same-requestId collapse:
// two CreateServiceInstance carrying the SAME instanceId but DIFFERENT
// requestIds — the first commits vtx.service.<id>, the second's CreateOnly
// write of that key conflicts (RevisionConflict) and is REJECTED. The second
// does NOT overwrite or silently no-op the first.
func TestCreateServiceInstance_IdCollision_DifferentRequestIds_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "id-collision")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBco11ideapp1cntHJKM"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	suppliedID := "CraceHand1eHJKMNPQRS"
	suppliedKey := "vtx.service." + suppliedID
	payload := json.RawMessage(`{"family":"backgroundCheck","instanceId":"` + suppliedID +
		`","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`)
	mkEnv := func(label string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CreateServiceInstance",
			Actor:         svcStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "service",
			Payload:       payload,
			ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
		}
	}

	// First create with this id commits.
	testutil.PublishOp(t, conn, mkEnv("collideFirst01"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	rev1 := readDoc(t, ctx, conn, suppliedKey)["lastModifiedByOp"]

	// Second create — DIFFERENT requestId, SAME instanceId. The CreateOnly
	// write of vtx.service.<id> conflicts (the key already exists), so the op
	// is rejected, NOT collapsed and NOT overwritten.
	testutil.PublishOp(t, conn, mkEnv("collideSecond1"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The committed instance is unchanged (no overwrite).
	rev2 := readDoc(t, ctx, conn, suppliedKey)["lastModifiedByOp"]
	if rev1 != rev2 {
		t.Fatalf("colliding second create must not overwrite the instance: %v vs %v", rev1, rev2)
	}
}

// TestCreateServiceInstance_ExtraSegmentKey_Rejected proves the strict
// vertex-key arity guard (parts_of rejects a non-3-segment key): a template /
// instanceKey carrying an extra trailing segment (4 segments, e.g.
// vtx.service.<id>.outcome) is rejected with InvalidArgument, not silently
// truncated to its first three segments.
func TestCreateServiceInstance_ExtraSegmentKey_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "extra-segment")

	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")

	applicantID := "BBxtrasegapp1cntHJKM"
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	// A 4-segment template key (extra ".outcome" tail). Pre-strict, parts_of
	// would have returned parts[2] and dropped the tail; now it rejects on
	// arity. The malformed key is deliberately NOT listed in ContextHint.Reads
	// — listing it would surface as a HydrationMiss before the script runs; we
	// want the op to REACH the script so the parts_of arity guard is what
	// rejects it. The script reads the payload field directly (Reads only
	// governs which keys are hydrated into state).
	badTemplate := tplKey + ".outcome"
	createEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("extraSegInst01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + badTemplate + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, createEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// And on RecordServiceOutcome's instanceKey: a 4-segment instance key is
	// likewise rejected by the strict parts_of guard. The base (3-segment) form
	// is a well-formed vertex key; the extra ".outcome" tail is what trips the
	// arity guard. Again the malformed key is NOT listed in Reads, so the
	// rejection is attributable to parts_of, not hydration.
	badInstance := "vtx.service.BBsomeinstkHJKMNPQRS.outcome"
	recEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("extraSegRec001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"instanceKey":"` + badInstance + `","status":"completed","completedAt":"2026-06-18T14:00:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, recEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRecordServiceOutcome_ZeroExpectedRevision_Rejected proves the
// expectedRevision <= 0 guard: an explicit expectedRevision of 0 (the substrate
// "key must not exist" sentinel, which can never match a live instance) is
// rejected with InvalidArgument. An ABSENT expectedRevision still means "no OCC
// guard" and is exercised by the happy-path D5 gate.
func TestRecordServiceOutcome_ZeroExpectedRevision_Rejected(t *testing.T) {
	ctx, conn := setupServiceEnv(t)
	cp, cons := newServicePipeline(t, ctx, conn, "rev-zero")

	instKey := createLiveInstance(t, ctx, conn, cp, cons, "BBzeroapp1cntHJKMNPQ")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("zeroRev000001"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceOutcome",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"instanceKey":"` + instKey + `","status":"completed","completedAt":"2026-06-18T14:00:00Z","expectedRevision":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{instKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// createLiveInstance is a test helper: creates a template + a live applicant
// identity (seeded under applicantID) and a backgroundCheck instance, returning
// the committed instance key. Used by the RecordServiceOutcome negative-path
// tests that need a live, outcome-less instance to reject against.
func createLiveInstance(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantID string) string {
	t.Helper()
	tplKey := createTemplate(t, ctx, conn, cp, cons, "backgroundCheck")
	applicantKey := "vtx.identity." + applicantID
	seedVertex(t, ctx, conn, applicantKey, "identity", map[string]any{"state": "claimed"})

	instReqID := testutil.GenReqID("liveInst" + applicantID[2:8])
	instID := nanoIDFromRequestID(instReqID)
	instKey := "vtx.service." + instID
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateServiceInstance",
		Actor:         svcStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "service",
		Payload:       json.RawMessage(`{"family":"backgroundCheck","template":"` + tplKey + `","providedTo":"` + applicantKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tplKey, applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return instKey
}
