// docGen externalTask triad tests through the real install + Processor
// pipeline (the lease_signing_test harness): CreateLeaseDocInstance mints the
// claim + assembles the document fields into external.docGen; the unsigned
// gate rejects with no claim; RecordLeaseDocOutcome records the
// pointer-carrying create-only .outcome + the completion event; the shared
// RecordServiceDispatch pending marker works against a docGen-family claim
// unchanged.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedAspect writes an aspect envelope directly into the harness Core bucket
// (the seedUnit .listing idiom) — used for fixture state no installed op mints
// in this suite (the applicant identity's .name, the unit's .address).
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, local, class string, data map[string]any) {
	t.Helper()
	body := map[string]any{
		"class": class, "isDeleted": false, "vertexKey": vertexKey, "localName": local,
		"data": data,
	}
	b, _ := json.Marshal(body)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vertexKey+"."+local, b); err != nil {
		t.Fatalf("seed aspect %s.%s: %v", vertexKey, local, err)
	}
}

// mintNamedIdentity creates a REAL identity through identity-domain's
// CreateUnclaimedIdentity — encrypted .name + .piiKey — so the docGen
// instanceOp's field assembly exercises kv.Read's decrypt-on-read, exactly as
// a live deployment resolves the tenant's name.
func mintNamedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	identityKey := "vtx.identity." + nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"` + name + `","email":"` + label + `@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return identityKey
}

// signedDocGenApp builds a signed application over applicantKey with a unit
// carrying .address + .listing, returning (appKey, unitKey).
func signedDocGenApp(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantKey string) (string, string) {
	t.Helper()
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	seedAspect(t, ctx, conn, unitKey, "address", "address", map[string]any{"line1": "123 Loft St", "city": "San Francisco", "region": "CA"})
	signLease(t, ctx, conn, cp, cons, "docgen-sign", appKey, "2026-07-01T12:00:00Z")
	return appKey, unitKey
}

// TestLeaseDocInstance_MintsClaim_EmitsDocGenEvent: the instanceOp validates
// the signed subject, mints the docGen claim (envelope class + instanceOf +
// providedTo→LEASEAPP), and emits external.docGen whose params carry the
// Processor-side-resolved document fields.
func TestLeaseDocInstance_MintsClaim_EmitsDocGenEvent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docinstop")

	applicantKey := mintNamedIdentity(t, ctx, conn, cp, cons, "docIdent00001", "Alice Smith")
	appKey, unitKey := signedDocGenApp(t, ctx, conn, cp, cons, applicantKey)
	appID := appKey[len("vtx.leaseapp."):]

	handle := "dgHandAbCdEfGhJkMnPq"
	instKey := "vtx.service." + handle
	instReqID := testutil.GenReqID("docInstOp0001")
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseDocInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseDocInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + appKey +
			`","adapter":"docGen","replyOp":"RecordLeaseDocOutcome","params":{"family":"docGen"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{appKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The claim vertex: key type `service`, envelope class service.docGen.instance
	// (P7 — no shadow aspects), root data {} (D5).
	instDoc := readDoc(t, ctx, conn, instKey)
	if cls, _ := instDoc["class"].(string); cls != "service.docGen.instance" {
		t.Fatalf("claim vertex class = %q, want service.docGen.instance", cls)
	}
	if data, _ := instDoc["data"].(map[string]any); len(data) != 0 {
		t.Fatalf("claim vertex root data must be minimal ({}), got %v", data)
	}

	// instanceOf → the leaseDocInstance type-authority meta (exactly one), and
	// providedTo → the LEASEAPP (the document is about the application).
	allKeys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	instOfPrefix := "lnk.service." + handle + ".instanceOf.meta."
	instOfCount := 0
	for _, k := range allKeys {
		if strings.HasPrefix(k, instOfPrefix) {
			instOfCount++
		}
	}
	if instOfCount != 1 {
		t.Fatalf("want exactly one instanceOf→meta link (prefix %q), got %d", instOfPrefix, instOfCount)
	}
	ptLnk := "lnk.service." + handle + ".providedTo.leaseapp." + appID
	ptDoc := readDoc(t, ctx, conn, ptLnk)
	if got, _ := ptDoc["sourceVertex"].(string); got != instKey {
		t.Fatalf("providedTo sourceVertex = %q, want %q", got, instKey)
	}
	if got, _ := ptDoc["targetVertex"].(string); got != appKey {
		t.Fatalf("providedTo targetVertex = %q, want %q", got, appKey)
	}

	// The external.docGen event: the bridge-reader shape plus the resolved doc.
	ev := findEmittedEvent(t, ctx, conn, instReqID, "external.docGen")
	for field, want := range map[string]string{
		"instanceKey": handle, "externalRef": handle, "idempotencyKey": handle,
		"adapter": "docGen", "replyOp": "RecordLeaseDocOutcome", "dispatchOp": "RecordServiceDispatch",
	} {
		if got, _ := ev[field].(string); got != want {
			t.Fatalf("external event %s = %q, want %q", field, got, want)
		}
	}
	params, _ := ev["params"].(map[string]any)
	if params == nil {
		t.Fatalf("external event params missing: %v", ev)
	}
	if got, _ := params["family"].(string); got != "docGen" {
		t.Fatalf("params.family = %q, want docGen", got)
	}
	if got, _ := params["leaseAppKey"].(string); got != appKey {
		t.Fatalf("params.leaseAppKey = %q, want %q", got, appKey)
	}
	doc, _ := params["doc"].(map[string]any)
	if doc == nil {
		t.Fatalf("params.doc missing: %v", params)
	}
	// tenantName is deliberately never assembled (sensitive-param-egress design
	// §3.6's emission guard — see leasedoc_scripts.go's CreateLeaseDocInstance
	// comment): a link-discovered sensitive aspect has no contextHint.egressReads
	// declaration path, so the DDL omits it rather than leak plaintext into the
	// durable external.docGen event.
	if _, present := doc["tenantName"]; present {
		t.Fatalf("doc.tenantName must be omitted (no egress-safe read path yet), got %v", doc["tenantName"])
	}
	wantStrings := map[string]string{
		"applicant":         applicantKey,
		"unitKey":           unitKey,
		"unitAddress":       "123 Loft St",
		"unitCity":          "San Francisco",
		"unitRegion":        "CA",
		"unitCurrency":      "USD",
		"unitAvailableFrom": "2026-08-01T00:00:00Z",
		"signedAt":          "2026-07-01T12:00:00Z",
	}
	for field, want := range wantStrings {
		if got, _ := doc[field].(string); got != want {
			t.Fatalf("doc.%s = %q, want %q (doc: %v)", field, got, want, doc)
		}
	}
	if got, _ := doc["unitRent"].(float64); got != 2400 {
		t.Fatalf("doc.unitRent = %v, want 2400", doc["unitRent"])
	}
	if got, _ := doc["unitLeaseTermMonths"].(float64); got != 12 {
		t.Fatalf("doc.unitLeaseTermMonths = %v, want 12", doc["unitLeaseTermMonths"])
	}
	// createApplication supplies no .terms — absent optional fields are omitted.
	if _, present := doc["termsMoveInDate"]; present {
		t.Fatalf("doc.termsMoveInDate must be omitted when the application has no .terms, got %v", doc["termsMoveInDate"])
	}
}

// submitShredIdentityKey drives a REAL ShredIdentityKey (privacy-base, urgent
// lane) against identityKey through its own pipeline/consumer pair — the
// caller must build that pipeline with the SAME Vault instance backing
// identityKey's encryption, so the vault-side shredded state the field-
// assembly script's decrypt-on-read observes is genuine, not a fixture.
func submitShredIdentityKey(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, identityKey, label string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestLeaseDocInstance_ShreddedApplicant_OmitsNameNoFailure: a crypto-shredded
// applicant's piiKey aspect stays PRESENT (privacy-base updates it in place,
// data.shredded=true) rather than being deleted, so the field-assembly
// script's probe must check the flag, not just presence. This drives a REAL
// ShredIdentityKey through the same Vault instance backing the applicant's
// encrypted .name, then proves CreateLeaseDocInstance still COMMITS (mints
// the claim, dispatches the event) with the document degrading to the bare
// applicant key instead of failing the op — the same degrade rule as an
// unnamed identity.
func TestLeaseDocInstance_ShreddedApplicant_OmitsNameNoFailure(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	v := testutil.TestVault(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "docshred", Instance: "ls-docshred", Vault: v,
	})
	urgentCP, urgentCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "docshredurg", Instance: "ls-docshredurg", Vault: v,
		FilterSubjects: []string{"ops.urgent"},
	})

	applicantKey := mintNamedIdentity(t, ctx, conn, cp, cons, "docShredIdent01", "Shredded Tenant")
	appKey, _ := signedDocGenApp(t, ctx, conn, cp, cons, applicantKey)
	submitShredIdentityKey(t, ctx, conn, urgentCP, urgentCons, applicantKey, "docShredOp0001")

	handle := "dgShredHandAbCdEfGhJ"
	instReqID := testutil.GenReqID("docShredInstOp01")
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseDocInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseDocInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + appKey +
			`","adapter":"docGen","replyOp":"RecordLeaseDocOutcome","params":{"family":"docGen"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{appKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	ev := findEmittedEvent(t, ctx, conn, instReqID, "external.docGen")
	params, _ := ev["params"].(map[string]any)
	doc, _ := params["doc"].(map[string]any)
	if doc == nil {
		t.Fatalf("params.doc missing: %v", params)
	}
	if _, present := doc["tenantName"]; present {
		t.Fatalf("doc.tenantName must be omitted for a shredded applicant, got %v", doc["tenantName"])
	}
	if got, _ := doc["applicant"].(string); got != applicantKey {
		t.Fatalf("doc.applicant = %q, want %q (the bare key a nameless render falls back to)", got, applicantKey)
	}
	if _, present := doc["unitAddress"]; !present {
		t.Fatalf("doc.unitAddress must still be present: a shredded applicant degrades only the name, not the rest of assembly")
	}
}

// TestLeaseDocInstance_UnsignedSubject_Rejected: the signature gate — an
// unsigned application fails the op with NO claim and NO dispatch.
func TestLeaseDocInstance_UnsignedSubject_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docinstunsigned")

	applicantKey := seedApplicant(t, ctx, conn, "BBdocapp2cntHJKMNPQR")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	handle := "dgUnsgnAbCdEfGhJkMnP"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("docInstUns001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseDocInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseDocInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + appKey +
			`","adapter":"docGen","replyOp":"RecordLeaseDocOutcome","params":{"family":"docGen"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{appKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if keyExists(t, ctx, conn, "vtx.service."+handle) {
		t.Fatalf("an unsigned application must mint NO claim vertex")
	}
}

// docPointerJSON is the reference adapter's completed Detail: the JSON
// document-pointer object the replyOp parses onto the .outcome aspect.
const docPointerJSON = `{\"digest\":\"SHA-256=abc123def456\",\"size\":1264,\"contentType\":\"text/plain; charset=utf-8\",\"storeName\":\"dgStoreNanoID1234567\",\"filename\":\"signed-lease-leaseapp.test.txt\"}`

// mintDocGenClaim runs the instanceOp for a fresh signed application (a
// seeded, keyless-name applicant — the reply/dispatch tests do not read the
// doc fields) and returns the claim-vertex key.
func mintDocGenClaim(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantID, handle string) string {
	t.Helper()
	applicantKey := seedApplicant(t, ctx, conn, applicantID)
	appKey, _ := signedDocGenApp(t, ctx, conn, cp, cons, applicantKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("docClaim" + handle[:5]),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseDocInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseDocInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + appKey +
			`","adapter":"docGen","replyOp":"RecordLeaseDocOutcome","params":{"family":"docGen"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{appKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.service." + handle
}

// TestLeaseDocReply_RecordsPointerOutcome_EmitsCompletion: the replyOp commits
// read-free (no ContextHint.Reads — the live bridge shape), writes the
// create-only pointer-carrying .outcome (class leaseDocOutcome, no validUntil),
// emits orchestration.externalTaskCompleted with the BARE handle, and rejects a
// second reply.
func TestLeaseDocReply_RecordsPointerOutcome_EmitsCompletion(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docreplyop")

	handle := "dgRepXyAbCdEfGhJkMnP"
	instKey := mintDocGenClaim(t, ctx, conn, cp, cons, "BBdocapp3cntHJKMNPQR", handle)

	replyReqID := testutil.GenReqID("docReply00001")
	replyEnv := &processor.OperationEnvelope{
		RequestID:     replyReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseDocOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-07-02T10:00:00Z",
		Class:         "leaseDocReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"` + docPointerJSON + `"}`),
	}
	testutil.PublishOp(t, conn, replyEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	if cls, _ := odoc["class"].(string); cls != "leaseDocOutcome" {
		t.Fatalf(".outcome class = %q, want leaseDocOutcome (the exact-class write gate)", cls)
	}
	odata, _ := odoc["data"].(map[string]any)
	for field, want := range map[string]string{
		"status":      "completed",
		"completedAt": "2026-07-02T10:00:00Z",
		"digest":      "SHA-256=abc123def456",
		"contentType": "text/plain; charset=utf-8",
		"storeName":   "dgStoreNanoID1234567",
		"filename":    "signed-lease-leaseapp.test.txt",
	} {
		if got, _ := odata[field].(string); got != want {
			t.Fatalf("outcome.%s = %q, want %q", field, got, want)
		}
	}
	if got, _ := odata["size"].(float64); got != 1264 {
		t.Fatalf("outcome.size = %v, want 1264", odata["size"])
	}
	if _, present := odata["validUntil"]; present {
		t.Fatalf("a docGen outcome must carry NO validUntil (a produced document does not expire), got %v", odata["validUntil"])
	}
	// D5: the claim-vertex root data stays {}.
	if d, _ := readDoc(t, ctx, conn, instKey)["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("claim vertex root data must stay minimal ({}), got %v", d)
	}

	completion := findEmittedEvent(t, ctx, conn, replyReqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the BARE handle %q", got, handle)
	}
	prov := findEmittedEvent(t, ctx, conn, replyReqID, "service.outcomeRecorded")
	if got, _ := prov["result"].(string); got == "" {
		t.Fatalf("service.outcomeRecorded must carry the raw result for provenance")
	}

	// A second reply is rejected by the create-only .outcome conflict (FR58).
	reply2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("docReply00002"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseDocOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-07-02T11:00:00Z",
		Class:         "leaseDocReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"` + docPointerJSON + `"}`),
	}
	testutil.PublishOp(t, conn, reply2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLeaseDocReply_Failed_NoPointers: a failed render records {status,
// completedAt} only — the reason stays off the aspect (provenance carries it) —
// and still emits the completion signal (Loom's token must advance).
func TestLeaseDocReply_Failed_NoPointers(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docreplyfail")

	handle := "dgFaXxAbCdEfGhJkMnPq"
	instKey := mintDocGenClaim(t, ctx, conn, cp, cons, "BBdocapp4cntHJKMNPQR", handle)

	replyReqID := testutil.GenReqID("docReplyF0001")
	replyEnv := &processor.OperationEnvelope{
		RequestID:     replyReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseDocOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-07-02T10:00:00Z",
		Class:         "leaseDocReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"failed","result":"lease-doc render failed: doc.signedAt is required"}`),
	}
	testutil.PublishOp(t, conn, replyEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	odata, _ := readDoc(t, ctx, conn, instKey+".outcome")["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "failed" {
		t.Fatalf("outcome.status = %q, want failed", got)
	}
	for _, field := range []string{"digest", "size", "contentType", "storeName", "filename", "result"} {
		if _, present := odata[field]; present {
			t.Fatalf("a failed outcome must carry no %s, got %v", field, odata[field])
		}
	}
	completion := findEmittedEvent(t, ctx, conn, replyReqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("a failed outcome still completes the externalTask (externalRef = %q, want %q)", got, handle)
	}
}

// TestLeaseDocReply_CompletedWithoutPointers_Rejected: a completed reply whose
// result is absent or not the pointer object is rejected — the aspect never
// records a completed outcome without the pointer set the lens/playbook read.
func TestLeaseDocReply_CompletedWithoutPointers_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docreplybadptr")

	handle := "dgBadPtrAbCdEfGhJkMn"
	instKey := mintDocGenClaim(t, ctx, conn, cp, cons, "BBdocapp5cntHJKMNPQR", handle)

	for i, payload := range []string{
		`{"externalRef":"` + handle + `","status":"completed"}`,
		`{"externalRef":"` + handle + `","status":"completed","result":"not a pointer object"}`,
		`{"externalRef":"` + handle + `","status":"completed","result":"{\"digest\":\"SHA-256=x\"}"}`,
	} {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("docReplyB000" + string(rune('1'+i))),
			Lane:          processor.LaneDefault,
			OperationType: "RecordLeaseDocOutcome",
			Actor:         lsActorKey,
			SubmittedAt:   "2026-07-02T10:00:00Z",
			Class:         "leaseDocReply",
			Payload:       json.RawMessage(payload),
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	}
	if keyExists(t, ctx, conn, instKey+".outcome") {
		t.Fatalf("no .outcome may commit off a malformed completed reply")
	}
}

// TestServiceDispatch_OnDocGenClaim_RecordsMarker: the SHARED RecordServiceDispatch
// pending path works against a docGen-family claim unchanged — the dispatchOp
// script and the leaseServiceDispatchMarker write gate reconstruct
// vtx.service.<handle> generically (no family/class knowledge), so a future
// async docGen vendor rides the §10.4 poll/timeout lane with no package change.
func TestServiceDispatch_OnDocGenClaim_RecordsMarker(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "docdispatch")

	handle := "dgPendXngAbCdEfGhJkM"
	instKey := mintDocGenClaim(t, ctx, conn, cp, cons, "BBdocapp6cntHJKMNPQR", handle)

	dispatchReqID := testutil.GenReqID("docDispatch01")
	env := &processor.OperationEnvelope{
		RequestID:     dispatchReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-07-02T10:00:00Z",
		Class:         "leaseServiceDispatch",
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","vendorRef":"vendor-dg-123","adapter":"docGen",` +
			`"replyOp":"RecordLeaseDocOutcome","nextPollAt":"2026-07-02T10:00:30Z","deadline":"2026-07-03T10:00:00Z"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	ddoc := readDoc(t, ctx, conn, instKey+".dispatch")
	if cls, _ := ddoc["class"].(string); cls != "leaseServiceDispatchMarker" {
		t.Fatalf(".dispatch class = %q, want leaseServiceDispatchMarker (the shared marker gate)", cls)
	}
	ddata, _ := ddoc["data"].(map[string]any)
	if got, _ := ddata["vendorRef"].(string); got != "vendor-dg-123" {
		t.Fatalf(".dispatch vendorRef = %q, want vendor-dg-123", got)
	}
	if got, _ := ddata["replyOp"].(string); got != "RecordLeaseDocOutcome" {
		t.Fatalf(".dispatch replyOp = %q, want RecordLeaseDocOutcome", got)
	}
	// The pending marker is NOT a completion: no .outcome, no completion event.
	if keyExists(t, ctx, conn, instKey+".outcome") {
		t.Fatalf("a pending dispatch must write no .outcome")
	}
	assertNoEmittedEvent(t, ctx, conn, dispatchReqID, "orchestration.externalTaskCompleted")
}
