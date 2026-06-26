// lease-signing integration tests through the real install + Processor pipeline.
//
// These tests live in an external test package (leasesigning_test) so they
// exercise the public Lattice surface a real Capability Package sees: seed the
// kernel, install rbac + identity + orchestration-base + service-domain +
// lease-signing through the Processor, then submit the ops and assert the
// committed Core-KV shape + the emitted events.
//
// AC #4: every outcome write is a DIRECT RecordLeaseServiceOutcome op with a
// synthetic {externalRef, result} payload (the bridge's shape) — never a live
// bridge process (that is 14.5).
package leasesigning_test

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
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
)

const (
	lsActorID  = "BBlsActorrHJKMNPQRST"
	lsActorKey = "vtx.identity." + lsActorID
	lsCapKey   = "cap.identity." + lsActorID
)

// lsCapDoc grants the test actor the lease-signing ops (scope any).
func lsCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    lsCapKey,
		Actor:                  lsActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{lsActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseApplication", Scope: "any"},
			{OperationType: "SignLease", Scope: "any"},
			{OperationType: "WithdrawLeaseApplication", Scope: "any"},
			{OperationType: "CreateLeaseServiceInstance", Scope: "any"},
			{OperationType: "RecordLeaseServiceOutcome", Scope: "any"},
			{OperationType: "RecordServiceDispatch", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// setupLeaseEnv seeds the kernel, installs the dependency chain +
// orchestration-base + service-domain + lease-signing, and seeds the cap doc.
func setupLeaseEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	installLeaseDeps(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, lsCapDoc())
	return ctx, conn
}

// installLeaseDeps installs orchestration-base, service-domain, then
// lease-signing through the real meta-install pipeline. The success of the
// lease-signing install IS the install round-trip proof (test 5): a malformed
// DDL self-description / playbook / pattern / a canonicalName collision fails
// here, before any op runs.
func installLeaseDeps(t *testing.T, ctx context.Context, conn *substrate.Conn) {
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
	if _, err := inst.Install(ctx, leasesigning.Package); err != nil {
		t.Fatalf("install lease-signing: %v", err)
	}
}

func newLeasePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ls-" + durable,
	})
}

func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
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

// seedApplicant seeds a live claimed identity to be the application's applicant.
func seedApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{"state": "claimed"})
	return key
}

// seedUnit seeds a live location-domain unit (vtx.unit.<id>, class=location) to
// be the application's leased unit. Seeded directly (not via location-domain's
// CreateLocation) because these package tests do not install location-domain;
// the leaseapp op only alive-checks the unit by key.
func seedUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.unit." + id
	seedVertex(t, ctx, conn, key, "location", map[string]any{})
	return key
}

// createApplication submits CreateLeaseApplication and returns the app key. It
// seeds a fresh live unit (vtx.unit.<id>) for the application to apply to (now
// required) and lists both the applicant and the unit in ContextHint.Reads.
func createApplication(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantKey string) string {
	t.Helper()
	reqID := testutil.GenReqID("createApp" + applicantKey[len(applicantKey)-4:])
	appID := nanoIDFromRequestID(reqID)
	// Reuse the applicant's (valid 20-char NanoID) id as the unit id: a distinct
	// key (vtx.unit.<id> vs vtx.identity.<id>) that still satisfies the link
	// key-pattern NanoID check the appliesToUnit link must pass.
	unitKey := seedUnit(t, ctx, conn, applicantKey[len("vtx.identity."):])
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{applicantKey, unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.leaseapp." + appID
}

// TestLeaseSigning_InstallRoundTrip_PlaybookAndPatternsValidate (test 5):
// installing the package end-to-end succeeds with the lens + weaverTarget + the
// three loomPatterns + the three DDLs + the op-metas present, and the expected
// meta-vertices land. setupLeaseEnv's install IS the round-trip; this test
// additionally asserts the meta-vertices committed.
func TestLeaseSigning_InstallRoundTrip_PlaybookAndPatternsValidate(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)

	// The lens meta-vertex carries the actorAggregate spec (lenses carry a
	// .canonicalName aspect).
	assertMetaByCanonical(t, ctx, conn, "meta.lens", "leaseApplicationComplete")
	// The weaverTarget + its .spec aspect (weaverTarget/loomPattern vertices
	// carry only a .spec aspect; their identity lives in the spec body).
	assertMetaBySpecField(t, ctx, conn, "meta.weaverTarget", "targetId", "leaseApplicationComplete")
	// The three loomPatterns + their .spec aspects.
	for _, pid := range []string{"backgroundCheck", "collectPayment", "onboarding"} {
		assertMetaBySpecField(t, ctx, conn, "meta.loomPattern", "patternId", pid)
	}
}

// assertMetaByCanonical scans the harness core bucket for a meta-vertex of the
// given class whose .canonicalName aspect matches name, returning its key.
func assertMetaByCanonical(t *testing.T, ctx context.Context, conn *substrate.Conn, class, name string) string {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		// canonicalName aspects are vtx.meta.<id>.canonicalName.
		if len(k) < len(".canonicalName") || k[len(k)-len(".canonicalName"):] != ".canonicalName" {
			continue
		}
		doc := readDoc(t, ctx, conn, k)
		data, _ := doc["data"].(map[string]any)
		if v, _ := data["value"].(string); v != name {
			continue
		}
		vtxKey := k[:len(k)-len(".canonicalName")]
		vdoc := readDoc(t, ctx, conn, vtxKey)
		if cls, _ := vdoc["class"].(string); cls == class {
			return vtxKey
		}
	}
	t.Fatalf("no %s meta-vertex with canonicalName %q found", class, name)
	return ""
}

// assertMetaBySpecField scans for a meta-vertex of the given class whose .spec
// aspect body carries specField == want, asserting both the vertex and its .spec
// aspect exist. Used for weaverTarget/loomPattern (which carry no .canonicalName
// aspect — their identity is in the spec body).
func assertMetaBySpecField(t *testing.T, ctx context.Context, conn *substrate.Conn, class, specField, want string) {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		if len(k) < len(".spec") || k[len(k)-len(".spec"):] != ".spec" {
			continue
		}
		vtxKey := k[:len(k)-len(".spec")]
		vdoc := readDoc(t, ctx, conn, vtxKey)
		if cls, _ := vdoc["class"].(string); cls != class {
			continue
		}
		doc := readDoc(t, ctx, conn, k)
		data, _ := doc["data"].(map[string]any)
		if v, _ := data[specField].(string); v == want {
			return
		}
	}
	t.Fatalf("no %s meta-vertex with .spec %s=%q found", class, specField, want)
}

// TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent (test 3): the
// externalTask instanceOp. Submit CreateLeaseServiceInstance and assert the
// claim vertex is minted (root {}, .class + .family aspects, providedTo link)
// and the external.<adapter> event was emitted with the bridge-reader shape.
func TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "instanceop")

	applicantKey := seedApplicant(t, ctx, conn, "BBinstapp1cntHJKMNPQ")
	applicantID := applicantKey[len("vtx.identity."):]

	handle := "afrqvygDz1chYFednoSV"
	instKey := "vtx.service." + handle
	instReqID := testutil.GenReqID("instOpBg00001")
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + applicantKey +
			`","adapter":"backgroundCheck","replyOp":"RecordLeaseServiceOutcome","params":{"family":"backgroundCheck"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the claim vertex: key type `service` (so the lens anchors on it) but
	// class leaseServiceInstance (package-owned, to avoid the service DDL's
	// permittedCommands restriction); root data {} (D5).
	instDoc := readDoc(t, ctx, conn, instKey)
	if cls, _ := instDoc["class"].(string); cls != "leaseServiceInstance" {
		t.Fatalf("claim vertex class = %q, want leaseServiceInstance", cls)
	}
	data, _ := instDoc["data"].(map[string]any)
	if len(data) != 0 {
		t.Fatalf("claim vertex root data must be minimal ({}), got %v", data)
	}
	// .class aspect (14.1 shape).
	cdoc := readDoc(t, ctx, conn, instKey+".class")
	cdata, _ := cdoc["data"].(map[string]any)
	if v, _ := cdata["value"].(string); v != "service.backgroundCheck.instance" {
		t.Fatalf(".class aspect = %q, want service.backgroundCheck.instance", v)
	}
	// .family aspect (the lens discriminator).
	fdoc := readDoc(t, ctx, conn, instKey+".family")
	fdata, _ := fdoc["data"].(map[string]any)
	if v, _ := fdata["value"].(string); v != "backgroundCheck" {
		t.Fatalf(".family aspect = %q, want backgroundCheck", v)
	}
	// providedTo link instance→identity.
	ptLnk := "lnk.service." + handle + ".providedTo.identity." + applicantID
	ptDoc := readDoc(t, ctx, conn, ptLnk)
	if got, _ := ptDoc["sourceVertex"].(string); got != instKey {
		t.Fatalf("providedTo sourceVertex = %q, want %q", got, instKey)
	}
	if got, _ := ptDoc["targetVertex"].(string); got != applicantKey {
		t.Fatalf("providedTo targetVertex = %q, want %q", got, applicantKey)
	}

	// (b) the external.backgroundCheck event was emitted with the bridge-reader
	// shape: instanceKey == externalRef == idempotencyKey == the bare handle.
	ev := findEmittedEvent(t, ctx, conn, instReqID, "external.backgroundCheck")
	if got, _ := ev["instanceKey"].(string); got != handle {
		t.Fatalf("external event instanceKey = %q, want %q", got, handle)
	}
	if got, _ := ev["externalRef"].(string); got != handle {
		t.Fatalf("external event externalRef = %q, want %q", got, handle)
	}
	if got, _ := ev["idempotencyKey"].(string); got != handle {
		t.Fatalf("external event idempotencyKey = %q, want %q", got, handle)
	}
	if got, _ := ev["adapter"].(string); got != "backgroundCheck" {
		t.Fatalf("external event adapter = %q, want backgroundCheck", got)
	}
	if got, _ := ev["replyOp"].(string); got != "RecordLeaseServiceOutcome" {
		t.Fatalf("external event replyOp = %q, want RecordLeaseServiceOutcome", got)
	}
	// The dispatchOp seam: the bridge posts this op if its adapter returns Pending.
	if got, _ := ev["dispatchOp"].(string); got != "RecordServiceDispatch" {
		t.Fatalf("external event dispatchOp = %q, want RecordServiceDispatch", got)
	}
}

// findEmittedEvent reads the committed transactional-outbox aspect for an op's
// requestId and returns the payload of the first event of the given class. The
// outbox aspect is the faithful EventList persisted in the step-8 atomic batch
// (the outbox consumer publishes from it) — reading it asserts the emission
// without running the outbox consumer in the test harness.
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

// assertNoEmittedEvent fails if an op's transactional-outbox aspect contains any
// event of the given class — the negative of findEmittedEvent (asserting a
// completion signal is NOT emitted on a pending dispatch).
func assertNoEmittedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) {
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
			t.Fatalf("op %s must NOT emit a %s event, but it did (events: %v)", requestID, class, eventClasses(ob.Data.Events))
		}
	}
}

// TestLeaseServiceReply_RecordsOutcome_EmitsExternalTaskCompleted (test 4 — THE
// §0.A trap; AC #3). Pre-create a claim vertex, submit RecordLeaseServiceOutcome
// the way the live bridge does — payload {externalRef, result} with NO
// ContextHint.Reads (the bridge's actuator sets none) — and assert: the op
// commits read-free; the .outcome aspect is written (status=completed, canonical
// completedAt, and NO result — the free-form result stays off the projection
// plane, D5 root {}); the op emits orchestration.externalTaskCompleted carrying
// the BARE handle; and a second reply is rejected by the create-only .outcome
// guard, also with no Reads.
func TestLeaseServiceReply_RecordsOutcome_EmitsExternalTaskCompleted(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "replyop")

	applicantKey := seedApplicant(t, ctx, conn, "BBrepapp1cntHJKMNPQR")
	handle := "JFLdWyJmg9A32jxPvDpw"
	instKey := "vtx.service." + handle

	// Mint the claim vertex via the instanceOp (the matched pair).
	instEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("replyInst0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + applicantKey +
			`","adapter":"backgroundCheck","replyOp":"RecordLeaseServiceOutcome","params":{"family":"backgroundCheck"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, instEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The replyOp exactly as the live bridge submits it: payload
	// {externalRef, result} and NO ContextHint.Reads (internal/bridge's actuator
	// builds an envelope with no Reads field). It must commit read-free.
	replyReqID := testutil.GenReqID("replyRec00001")
	replyEnv := &processor.OperationEnvelope{
		RequestID:     replyReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T14:00:00Z",
		Class:         "leaseServiceReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"background-check cleared for ` + applicantKey + `"}`),
	}
	testutil.PublishOp(t, conn, replyEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the .outcome aspect — status=completed, canonical completedAt derived
	// from op.submittedAt. The free-form result is NOT written to the aspect (it
	// rides the service.outcomeRecorded provenance event instead).
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "completed" {
		t.Fatalf("outcome.status = %q, want completed", got)
	}
	if got, _ := odata["completedAt"].(string); got != "2026-06-18T14:00:00Z" {
		t.Fatalf("outcome.completedAt = %q, want canonical 2026-06-18T14:00:00Z", got)
	}
	// validUntil = completedAt + the bgcheck freshness window, stamped by the
	// read-free replyOp via time.rfc3339_add (pure arithmetic on completedAt, no
	// clock). The demo window is "5m" (see scripts.go bgcheckFreshnessWindow), so
	// 14:00:00Z + 5m = 14:05:00Z. If 14.5 tunes the window, update this constant.
	if got, _ := odata["validUntil"].(string); got != "2026-06-18T14:05:00Z" {
		t.Fatalf("outcome.validUntil = %q, want completedAt + 5m window = 2026-06-18T14:05:00Z", got)
	}
	if _, present := odata["result"]; present {
		t.Fatalf("outcome aspect must NOT carry the free-form result (PII off the projection plane), got %v", odata["result"])
	}
	// (c) D5: the claim-vertex root data stays {}.
	instDoc := readDoc(t, ctx, conn, instKey)
	if d, _ := instDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("claim vertex root data must stay minimal ({}), got %v", d)
	}

	// (b) THE TRAP: the op emits orchestration.externalTaskCompleted carrying the
	// BARE handle as externalRef (not the full vtx key). Without this the
	// externalTask never completes.
	completion := findEmittedEvent(t, ctx, conn, replyReqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the BARE handle %q", got, handle)
	}
	if completion["externalRef"] == instKey {
		t.Fatalf("externalTaskCompleted externalRef must be the bare handle, not the full vtx key")
	}

	// (b') the free-form result rides the provenance event body (kept off the
	// projection-plane aspect).
	prov := findEmittedEvent(t, ctx, conn, replyReqID, "service.outcomeRecorded")
	if got, _ := prov["result"].(string); got == "" {
		t.Fatalf("service.outcomeRecorded must carry the free-form result for provenance, got empty")
	}

	// (d) a second reply for the same handle is rejected by the create-only
	// .outcome conflict — the FR58 redelivery defense at the DDL layer. The
	// bridge submits no Reads (mirrored here), so the rejection is the batch
	// conflict on the already-existing .outcome key, NOT a state-read guard.
	reply2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("replyRec00002"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T15:00:00Z",
		Class:         "leaseServiceReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"second attempt"}`),
	}
	testutil.PublishOp(t, conn, reply2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLeaseServiceDispatch_RecordsPendingMarker_NoCompletion: the bridge submits
// RecordServiceDispatch when its adapter returns Pending — payload
// {externalRef, vendorRef} with NO ContextHint.Reads (the bridge's actuator sets
// none). It must commit read-free; the .dispatch aspect is written
// {vendorRef, submittedAt} (D5 root {}); NO .outcome aspect is written; and it
// emits NO orchestration.externalTaskCompleted (the task is not done — the token
// stays parked), only the service.dispatchRecorded provenance. A second dispatch
// for the same handle is rejected by the create-only .dispatch guard.
func TestLeaseServiceDispatch_RecordsPendingMarker_NoCompletion(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dispatchop")

	handle := "pendHwK4rqZbVnCdLxYj"
	instKey := "vtx.service." + handle
	vendorRef := "vendor-ref-pending-001"
	adapter := "backgroundCheck"
	replyOp := "RecordLeaseServiceOutcome"
	nextPollAt := "2026-06-19T10:00:30Z"
	deadline := "2026-06-20T10:00:00Z"

	reqID := testutil.GenReqID("dispatchRec01")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T10:00:00Z",
		Class:         "leaseServiceDispatch",
		// No Reads — exactly as the bridge submits.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","vendorRef":"` + vendorRef +
			`","adapter":"` + adapter + `","replyOp":"` + replyOp +
			`","nextPollAt":"` + nextPollAt + `","deadline":"` + deadline + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the .dispatch aspect — {vendorRef, adapter, replyOp, submittedAt
	// (canonical-UTC of op.submittedAt), nextPollAt, deadline}.
	ddoc := readDoc(t, ctx, conn, instKey+".dispatch")
	ddata, _ := ddoc["data"].(map[string]any)
	if got, _ := ddata["vendorRef"].(string); got != vendorRef {
		t.Fatalf("dispatch.vendorRef = %q, want %q", got, vendorRef)
	}
	if got, _ := ddata["submittedAt"].(string); got != "2026-06-19T10:00:00Z" {
		t.Fatalf("dispatch.submittedAt = %q, want canonical 2026-06-19T10:00:00Z", got)
	}
	if got, _ := ddata["adapter"].(string); got != adapter {
		t.Fatalf("dispatch.adapter = %q, want %q", got, adapter)
	}
	if got, _ := ddata["replyOp"].(string); got != replyOp {
		t.Fatalf("dispatch.replyOp = %q, want %q", got, replyOp)
	}
	if got, _ := ddata["nextPollAt"].(string); got != nextPollAt {
		t.Fatalf("dispatch.nextPollAt = %q, want %q", got, nextPollAt)
	}
	if got, _ := ddata["deadline"].(string); got != deadline {
		t.Fatalf("dispatch.deadline = %q, want %q", got, deadline)
	}

	// (b) NO .outcome aspect — the call is pending, not terminal (the token stays parked).
	if keyExists(t, ctx, conn, instKey+".outcome") {
		t.Fatalf("a pending dispatch must NOT write the .outcome aspect")
	}

	// (c) D5: the claim-vertex root data stays {} (the instanceOp minted it {}; the
	// dispatch op reconstructs the key read-free and does not touch the root).
	if keyExists(t, ctx, conn, instKey) {
		instDoc := readDoc(t, ctx, conn, instKey)
		if d, _ := instDoc["data"].(map[string]any); len(d) != 0 {
			t.Fatalf("claim vertex root data must stay minimal ({}), got %v", d)
		}
	}

	// (d) NO orchestration.externalTaskCompleted — Loom must NOT close the token on
	// a dispatch. Only the service.dispatchRecorded provenance is emitted.
	assertNoEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	prov := findEmittedEvent(t, ctx, conn, reqID, "service.dispatchRecorded")
	if got, _ := prov["vendorRef"].(string); got != vendorRef {
		t.Fatalf("service.dispatchRecorded vendorRef = %q, want %q", got, vendorRef)
	}

	// (e) a second dispatch for the same handle is rejected by the create-only
	// .dispatch conflict (the once-only guarantee at the DDL layer). The bridge
	// submits no Reads (mirrored here), so the rejection is the batch conflict on
	// the already-existing .dispatch key.
	dispatch2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("dispatchRec02"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T11:00:00Z",
		Class:         "leaseServiceDispatch",
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","vendorRef":"vendor-ref-pending-002"` +
			`,"adapter":"` + adapter + `","replyOp":"` + replyOp +
			`","nextPollAt":"` + nextPollAt + `","deadline":"` + deadline + `"}`),
	}
	testutil.PublishOp(t, conn, dispatch2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLeaseServiceDispatch_VendorRefRequired_Rejected: vendorRef is REQUIRED. A
// dispatch with no vendorRef is rejected (InvalidArgument), read-free, and writes
// no .dispatch aspect.
func TestLeaseServiceDispatch_VendorRefRequired_Rejected(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dispatch-vendorref-required")

	handle := "missVendorRefHandl9k"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("dispatchMiss1"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T12:00:00Z",
		Class:         "leaseServiceDispatch",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, "vtx.service."+handle+".dispatch") {
		t.Fatalf("a rejected dispatch must not write the .dispatch aspect")
	}
}

// TestLeaseServiceReply_FailedStatus_RecordsFailedOutcome: the bridge reply
// carries the adapter's terminal status=failed (a definitive business rejection,
// e.g. a declined charge / a failed background check — NOT a transient error,
// which the bridge Naks and never replies on). The replyOp writes the .outcome
// aspect {status: failed, completedAt} read-free and still emits the completion +
// provenance events. The free-form result stays OFF the projection-plane aspect.
func TestLeaseServiceReply_FailedStatus_RecordsFailedOutcome(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-failed")

	handle := "fT8kPmW2rqZbVnCdLxYj"
	instKey := "vtx.service." + handle
	reqID := testutil.GenReqID("replyFail0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T19:00:00Z",
		Class:         "leaseServiceReply",
		// No Reads — exactly as the bridge submits.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","status":"failed","result":"background-check declined for vtx.identity.x"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The .outcome aspect records status=failed (the lens reads this as the
	// service NOT having converged).
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "failed" {
		t.Fatalf("outcome.status = %q, want failed", got)
	}
	// The free-form result stays off the projection-plane aspect (PII discipline).
	if _, present := odata["result"]; present {
		t.Fatalf("outcome aspect must NOT carry the free-form result, got %v", odata["result"])
	}
	// The completion signal is still emitted on a failed outcome (the externalTask
	// completes — a definitive failure IS a completion).
	completion := findEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the bare handle %q", got, handle)
	}
}

// TestLeaseServiceReply_StatusRequired_Rejected: status is REQUIRED with no
// default. A reply with no status (the old bridge shape) and a reply with an
// out-of-enum status are both rejected (InvalidArgument), read-free.
func TestLeaseServiceReply_StatusRequired_Rejected(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-status-required")

	cases := []struct {
		name    string
		handle  string
		reqTag  string
		payload string
	}{
		{
			name:    "missing status",
			handle:  "missStatHandl3aBcDeF",
			reqTag:  "replyMiss00001",
			payload: `{"externalRef":"missStatHandl3aBcDeF","result":"x"}`,
		},
		{
			name:    "invalid status",
			handle:  "badStatusHandl9wXyZk",
			reqTag:  "replyBad000001",
			payload: `{"externalRef":"badStatusHandl9wXyZk","status":"maybe","result":"x"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID(tc.reqTag),
				Lane:          processor.LaneDefault,
				OperationType: "RecordLeaseServiceOutcome",
				Actor:         lsActorKey,
				SubmittedAt:   "2026-06-18T20:00:00Z",
				Class:         "leaseServiceReply",
				Payload:       json.RawMessage(tc.payload),
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
			// No .outcome aspect was written (the op was rejected before any mutation).
			if keyExists(t, ctx, conn, "vtx.service."+tc.handle+".outcome") {
				t.Fatalf("a rejected reply must not write the .outcome aspect")
			}
		})
	}
}

// TestLeaseServiceReply_ReadFree_CommitsWithoutHydration: the replyOp is
// read-free by design (the bridge submits no ContextHint.Reads), so it does not
// depend on the claim vertex being hydrated — it derives inst_key from the bare
// handle and writes the create-only .outcome aspect regardless. This is the
// faithful live-bridge path: the bridge only ever replies to instances it
// created, so there is no "unknown instance" guard to fire (Fix dropped the
// vertex_alive / .class checks that referenced unhydrated state). The op commits
// and emits the completion signal.
func TestLeaseServiceReply_ReadFree_CommitsWithoutHydration(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-readfree")

	handle := "uWm47ejkmzurjtX69AKL"
	instKey := "vtx.service." + handle
	reqID := testutil.GenReqID("replyRf000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T18:00:00Z",
		Class:         "leaseServiceReply",
		// No Reads — exactly as the bridge submits. The op reads no state.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"x"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The .outcome aspect is written read-free.
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "completed" {
		t.Fatalf("outcome.status = %q, want completed", got)
	}
	// The completion signal is emitted (the load-bearing externalTask close).
	completion := findEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the bare handle %q", got, handle)
	}
}

// TestCreateLeaseApplication_RootMinimal_LinkSentenceValid (AC #1 + D5): the
// application root data is {} and the applicationFor link is sentence-valid
// (leaseapp is the source, identity the target).
func TestCreateLeaseApplication_RootMinimal_LinkSentenceValid(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app")

	applicantKey := seedApplicant(t, ctx, conn, "BBcrapp1cantHJKMNPQR")
	applicantID := applicantKey[len("vtx.identity."):]
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	appID := appKey[len("vtx.leaseapp."):]

	appDoc := readDoc(t, ctx, conn, appKey)
	if cls, _ := appDoc["class"].(string); cls != "leaseapp" {
		t.Fatalf("application class = %q, want leaseapp", cls)
	}
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must be minimal ({}), got %v", d)
	}
	lnk := "lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID
	ldoc := readDoc(t, ctx, conn, lnk)
	if got, _ := ldoc["sourceVertex"].(string); got != appKey {
		t.Fatalf("applicationFor sourceVertex = %q, want %q (leaseapp is source)", got, appKey)
	}
	if got, _ := ldoc["targetVertex"].(string); got != applicantKey {
		t.Fatalf("applicationFor targetVertex = %q, want %q (identity is target)", got, applicantKey)
	}
}

// TestCreateLeaseApplication_UnknownApplicant_Rejected: an application for a
// non-existent applicant is rejected (no-orphan, FR29).
func TestCreateLeaseApplication_UnknownApplicant_Rejected(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-orphan")

	missing := "vtx.identity.BBnxapp1cantHJKMNPQR"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("appOrphan0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		// The missing applicant is NOT listed in Reads (a non-existent listed key
		// is a hydration miss); omitting it lets the op reach the script where the
		// UnknownApplicant guard rejects.
		Payload:     json.RawMessage(`{"applicant":"` + missing + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateLeaseApplication_AppliesToUnit_LinkSentenceValid (Increment 2): an
// application requires a live unit, writes the appliesToUnit link (leaseapp is
// source, unit is target — sentence-valid, Contract #1 §1.1), and writes the
// optional .terms aspect when moveInDate is supplied (root data stays {} — D5).
func TestCreateLeaseApplication_AppliesToUnit_LinkSentenceValid(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-unit")

	applicantKey := seedApplicant(t, ctx, conn, "BBunitapp1cntHJKMNPQ")
	unitKey := seedUnit(t, ctx, conn, "BBunitvtx1cntHJKMNPQ")
	unitID := unitKey[len("vtx.unit."):]

	reqID := testutil.GenReqID("appUnit000001")
	appID := nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `","moveInDate":"2026-08-01","leaseTermMonths":12,"requestedRent":2400}`),
		ContextHint:   &processor.ContextHint{Reads: []string{applicantKey, unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	appKey := "vtx.leaseapp." + appID

	lnk := "lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID
	ldoc := readDoc(t, ctx, conn, lnk)
	if got, _ := ldoc["sourceVertex"].(string); got != appKey {
		t.Fatalf("appliesToUnit sourceVertex = %q, want %q (leaseapp is source)", got, appKey)
	}
	if got, _ := ldoc["targetVertex"].(string); got != unitKey {
		t.Fatalf("appliesToUnit targetVertex = %q, want %q (unit is target)", got, unitKey)
	}

	tdoc := readDoc(t, ctx, conn, appKey+".terms")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["moveInDate"].(string); got != "2026-08-01" {
		t.Fatalf("terms.moveInDate = %q, want 2026-08-01", got)
	}
	appDoc := readDoc(t, ctx, conn, appKey)
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must stay minimal ({}), got %v", d)
	}
}

// TestCreateLeaseApplication_UnknownUnit_Rejected: an application naming a
// non-existent unit is rejected (no-orphan; unit is required + alive-checked).
func TestCreateLeaseApplication_UnknownUnit_Rejected(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-unit-orphan")

	applicantKey := seedApplicant(t, ctx, conn, "BBnxunitapp1HJKMNPQR")
	missingUnit := "vtx.unit.BBnxunitvtxcntHJKMNP"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("unitOrphan001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		// The applicant IS alive (listed in Reads); the unit is missing and NOT
		// listed (a non-existent listed key is a hydration miss), so the op reaches
		// the script where the UnknownUnit guard rejects.
		Payload:     json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + missingUnit + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// applyToUnit submits CreateLeaseApplication for applicantKey against a
// caller-supplied unitKey (so multiple applications can target the SAME unit —
// the per-unit duplicate-guard surface) and drives it to want. On
// OutcomeAccepted it returns the new app key; otherwise "". label must be
// unique per call (the request id — and so the minted app id — is deterministic
// from it).
func applyToUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, applicantKey, unitKey string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	appID := nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{applicantKey, unitKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	if want == processor.OutcomeAccepted {
		return "vtx.leaseapp." + appID
	}
	return ""
}

// TestCreateLeaseApplication_DuplicateSameApplicantSameUnit_Rejected: the
// reported bug — one applicant applying twice to the SAME unit must be rejected
// (DuplicateApplication), so a unit never accumulates duplicate live
// applications for one applicant (the bare-shell that pinned Weaver red).
func TestCreateLeaseApplication_DuplicateSameApplicantSameUnit_Rejected(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dup-same-applicant")

	applicant := seedApplicant(t, ctx, conn, "BBDUPAAAHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBDUPUUUHJKMNPQRSTUV")

	first := applyToUnit(t, ctx, conn, cp, cons, "dupFirstAAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	// The unit's index now holds the first application.
	idxDoc := readDoc(t, ctx, conn, unit+".leaseApplications")
	idxData, _ := idxDoc["data"].(map[string]any)
	if apps, _ := idxData["applications"].([]any); len(apps) != 1 {
		t.Fatalf("index should hold 1 application after first apply, got %v", idxData["applications"])
	}
	// Same applicant, same unit, second time → rejected.
	applyToUnit(t, ctx, conn, cp, cons, "dupSecondBBB", applicant, unit, processor.OutcomeRejected)
}

// TestCreateLeaseApplication_DifferentApplicantsSameUnit_Allowed: two DIFFERENT
// applicants applying to one unit both commit — normal leasing (the landlord
// chooses among applicants); the guard is per-applicant, not a unit lock.
func TestCreateLeaseApplication_DifferentApplicantsSameUnit_Allowed(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "diff-applicants")

	alice := seedApplicant(t, ctx, conn, "BBDFAAAAHJKMNPQRSTUV")
	bob := seedApplicant(t, ctx, conn, "BBDFBBBBHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBDFUUUUHJKMNPQRSTUV")

	a := applyToUnit(t, ctx, conn, cp, cons, "diffAliceAAA", alice, unit, processor.OutcomeAccepted)
	b := applyToUnit(t, ctx, conn, cp, cons, "diffBobBBBBB", bob, unit, processor.OutcomeAccepted)
	if a == "" || b == "" || a == b {
		t.Fatalf("both distinct-applicant applications should commit to distinct keys; got a=%q b=%q", a, b)
	}
	// The unit's index now holds BOTH applications.
	idxDoc := readDoc(t, ctx, conn, unit+".leaseApplications")
	idxData, _ := idxDoc["data"].(map[string]any)
	apps, _ := idxData["applications"].([]any)
	if len(apps) != 2 {
		t.Fatalf("index should hold 2 applications (alice + bob), got %v", apps)
	}
	seen := map[string]bool{}
	for _, e := range apps {
		em, _ := e.(map[string]any)
		seen[em["applicant"].(string)] = true
	}
	if !seen[alice] || !seen[bob] {
		t.Fatalf("index should carry both applicants; got %v", seen)
	}
}

// TestCreateLeaseApplication_SameApplicantDifferentUnits_Allowed: one applicant
// may apply to two DIFFERENT units (the index is per-unit).
func TestCreateLeaseApplication_SameApplicantDifferentUnits_Allowed(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "same-app-diff-unit")

	applicant := seedApplicant(t, ctx, conn, "BBMUAPPAHJKMNPQRSTUV")
	unit1 := seedUnit(t, ctx, conn, "BBMUUNNAHJKMNPQRSTUV")
	unit2 := seedUnit(t, ctx, conn, "BBMUUNNBHJKMNPQRSTUV")

	a := applyToUnit(t, ctx, conn, cp, cons, "multiUnit1AA", applicant, unit1, processor.OutcomeAccepted)
	b := applyToUnit(t, ctx, conn, cp, cons, "multiUnit2BB", applicant, unit2, processor.OutcomeAccepted)
	if a == "" || b == "" {
		t.Fatalf("same applicant applying to two different units should both commit; got a=%q b=%q", a, b)
	}
}

// TestCreateLeaseApplication_TombstonedPriorApplication_AllowsReapply: a
// withdrawn (tombstoned) application is pruned from the index and does NOT block
// the same applicant re-applying to the same unit.
func TestCreateLeaseApplication_TombstonedPriorApplication_AllowsReapply(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tombstone-reapply")

	applicant := seedApplicant(t, ctx, conn, "BBTMAPPAHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBTMUUUUHJKMNPQRSTUV")

	first := applyToUnit(t, ctx, conn, cp, cons, "tombFirstAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	// Logically tombstone the first application (a withdrawal): overwrite its
	// vertex envelope with isDeleted=true (the guard prunes on the vertex flag).
	tomb := map[string]any{"class": "leaseapp", "isDeleted": true, "data": map[string]any{}}
	tb, _ := json.Marshal(tomb)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, first, tb); err != nil {
		t.Fatalf("tombstone first application: %v", err)
	}
	// Re-apply: the dead first application is pruned, so this is allowed.
	second := applyToUnit(t, ctx, conn, cp, cons, "tombSecondBB", applicant, unit, processor.OutcomeAccepted)
	if second == "" {
		t.Fatalf("re-application after withdrawal should commit")
	}
	// The rebuilt index holds only the live re-application (the dead one pruned).
	idxDoc := readDoc(t, ctx, conn, unit+".leaseApplications")
	idxData, _ := idxDoc["data"].(map[string]any)
	apps, _ := idxData["applications"].([]any)
	if len(apps) != 1 {
		t.Fatalf("index should hold exactly the live re-application (dead pruned), got %v", apps)
	}
	em, _ := apps[0].(map[string]any)
	if em["leaseApp"].(string) != second {
		t.Fatalf("index entry = %v, want the live re-application %q", em, second)
	}
}

// withdraw submits WithdrawLeaseApplication{leaseAppKey, unit} (class leaseapp,
// reads=[leaseAppKey]) and asserts the outcome.
func withdraw(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unitKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "WithdrawLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","unit":"` + unitKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// indexApps returns the unit's .leaseApplications index entries (nil if absent).
func indexApps(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) []any {
	t.Helper()
	doc := readDoc(t, ctx, conn, unitKey+".leaseApplications")
	data, _ := doc["data"].(map[string]any)
	apps, _ := data["applications"].([]any)
	return apps
}

// TestWithdrawLeaseApplication drives the real withdraw op: a wrong unit is
// rejected (UnitMismatch) without tombstoning; the correct withdraw tombstones the
// application AND prunes the unit index; the applicant can then re-apply to the
// same unit; an unknown / already-withdrawn application is rejected.
func TestWithdrawLeaseApplication(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "withdraw")

	applicant := seedApplicant(t, ctx, conn, "BBWDAPPAHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBWDUUUUHJKMNPQRSTUV")
	otherUnit := seedUnit(t, ctx, conn, "BBWDOTHRHJKMNPQRSTUV")

	first := applyToUnit(t, ctx, conn, cp, cons, "wdFirstAAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	if apps := indexApps(t, ctx, conn, unit); len(apps) != 1 {
		t.Fatalf("index should hold 1 application before withdraw, got %v", apps)
	}

	// Wrong unit → UnitMismatch (rejected), and the application is NOT tombstoned.
	withdraw(t, ctx, conn, cp, cons, "wdWrongUnit", first, otherUnit, processor.OutcomeRejected)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); d {
		t.Fatalf("a wrong-unit withdraw must NOT tombstone the application")
	}

	// Correct unit → Accepted: tombstoned + index pruned.
	withdraw(t, ctx, conn, cp, cons, "wdCorrect01", first, unit, processor.OutcomeAccepted)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); !d {
		t.Fatalf("withdraw must tombstone the application")
	}
	if apps := indexApps(t, ctx, conn, unit); len(apps) != 0 {
		t.Fatalf("index should be empty after withdraw (pruned), got %v", apps)
	}

	// Re-apply (same applicant, same unit) → Accepted: the withdrawal unblocked it.
	second := applyToUnit(t, ctx, conn, cp, cons, "wdReapply01", applicant, unit, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-application after withdrawal should commit to a new key; got %q (first=%q)", second, first)
	}
	if apps := indexApps(t, ctx, conn, unit); len(apps) != 1 {
		t.Fatalf("index should hold exactly the re-application, got %v", apps)
	}

	// Double-withdraw the now-tombstoned first application → Rejected (UnknownLeaseApplication).
	withdraw(t, ctx, conn, cp, cons, "wdDouble001", first, unit, processor.OutcomeRejected)
}

// TestSignLease_WritesSignatureAspect (test 8 — the assignTask gap closure; D5).
// SignLease writes the .signature aspect (root stays {}); a second SignLease is
// rejected (once-only).
func TestSignLease_WritesSignatureAspect(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "sign")

	applicantKey := seedApplicant(t, ctx, conn, "BBsignapp1cntHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	// Before SignLease: no .signature aspect (missing_signature would be true).
	if keyExists(t, ctx, conn, appKey+".signature") {
		t.Fatalf(".signature aspect must not exist before SignLease")
	}

	signEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("sign000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T16:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey}},
	}
	testutil.PublishOp(t, conn, signEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	sdoc := readDoc(t, ctx, conn, appKey+".signature")
	sdata, _ := sdoc["data"].(map[string]any)
	if got, _ := sdata["signedAt"].(string); got != "2026-06-18T16:00:00Z" {
		t.Fatalf("signature.signedAt = %q, want canonical 2026-06-18T16:00:00Z", got)
	}
	// D5: the application root data stays {}.
	appDoc := readDoc(t, ctx, conn, appKey)
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must stay minimal ({}) after sign, got %v", d)
	}

	// A second SignLease is rejected (the .signature CreateOnly once-only guard).
	sign2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("sign000000002"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T17:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".signature"}},
	}
	testutil.PublishOp(t, conn, sign2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// keep the identity-domain dependency reference resolved.
	_ = identitydomain.Package
}
